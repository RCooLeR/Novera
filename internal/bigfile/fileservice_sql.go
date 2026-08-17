package bigfile

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/fileio"
	sqlanalyze "novera/internal/bigfile/plugins/sql/analyze"
	sqlextract "novera/internal/bigfile/plugins/sql/extract"
	sqlreshape "novera/internal/bigfile/plugins/sql/reshape"
	sqlschemadiff "novera/internal/bigfile/plugins/sql/schemadiff"
	"novera/internal/bigfile/replace"
	"novera/internal/bigfile/session"
	"novera/internal/bigfile/units"
)

var (
	ErrSQLCleanupPresetsDisabled     = errors.New("SQL cleanup presets are disabled until token-aware transformations preserve structured SQL and serialized values")
	ErrSQLRegexReplaceUnsupported    = errors.New("regex SQL replacement is unavailable for serialization-aware replacement; use plain find/replace so PHP/WordPress byte lengths can be recalculated")
	ErrSQLReplaceEncodingUnsupported = errors.New("serialization-aware SQL replacement currently requires a non-binary UTF-8 SQL dump")
	ErrSQLAnalysisRequired           = errors.New("analyze the dump first")
	ErrSQLAnalysisStale              = errors.New("SQL analysis is stale; analyze the current dump again")
	ErrSQLSchemaDiffBudget           = errors.New("SQL schema diff exceeds its bounded input or retained-memory budget")

	sqlSaveDialog = saveDialog
	sqlDirDialog  = dirDialog
)

var sqlSchemaDiffReadHook struct {
	sync.RWMutex
	fn func(context.Context, string, int64, int64) error
}

var sqlSchemaDiffLeaseHook struct {
	sync.RWMutex
	fn func(string)
}

func notifySQLSchemaDiffRead(ctx context.Context, fileID string, start, end int64) error {
	sqlSchemaDiffReadHook.RLock()
	hook := sqlSchemaDiffReadHook.fn
	sqlSchemaDiffReadHook.RUnlock()
	if hook == nil {
		return nil
	}
	return hook(ctx, fileID, start, end)
}

func installSQLSchemaDiffReadHook(hook func(context.Context, string, int64, int64) error) func() {
	sqlSchemaDiffReadHook.Lock()
	previous := sqlSchemaDiffReadHook.fn
	sqlSchemaDiffReadHook.fn = hook
	sqlSchemaDiffReadHook.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			sqlSchemaDiffReadHook.Lock()
			sqlSchemaDiffReadHook.fn = previous
			sqlSchemaDiffReadHook.Unlock()
		})
	}
}

func notifySQLSchemaDiffLease(fileID string) {
	sqlSchemaDiffLeaseHook.RLock()
	hook := sqlSchemaDiffLeaseHook.fn
	sqlSchemaDiffLeaseHook.RUnlock()
	if hook != nil {
		hook(fileID)
	}
}

func installSQLSchemaDiffLeaseHook(hook func(string)) func() {
	sqlSchemaDiffLeaseHook.Lock()
	previous := sqlSchemaDiffLeaseHook.fn
	sqlSchemaDiffLeaseHook.fn = hook
	sqlSchemaDiffLeaseHook.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			sqlSchemaDiffLeaseHook.Lock()
			sqlSchemaDiffLeaseHook.fn = previous
			sqlSchemaDiffLeaseHook.Unlock()
		})
	}
}

func sqlAnalysisAfterDialogError(err error) error {
	if errors.Is(err, ErrSQLAnalysisRequired) {
		return errors.Join(ErrSQLAnalysisStale, err)
	}
	return err
}

// SqlTable is one discovered table in a dump.
type SqlTable struct {
	Name         string `json:"name"`
	CreateOffset int64  `json:"createOffset"`
	InsertOffset int64  `json:"insertOffset"`
	Bytes        int64  `json:"bytes"` // approx byte size of the table's region in the dump
}

// SqlSummaryResult is the result of analysing a SQL dump.
type SqlSummaryResult struct {
	Tables       []SqlTable `json:"tables"`
	CreateTables int        `json:"createTables"`
	InsertTables int        `json:"insertTables"`
	DefinerCount int        `json:"definerCount"`
	Header       bool       `json:"header"`
}

type cachedSQLSummary struct {
	Generation uint64
	Summary    sqlanalyze.Summary
	Digest     [sha256.Size]byte
	Verified   bool
	LastUsed   uint64
}

const maxCachedSQLSummaries = 4

func (s *FileService) invalidateSQLSummary(fileID string) {
	s.sqlMu.Lock()
	delete(s.sqlSummary, fileID)
	s.sqlMu.Unlock()
}

func (s *FileService) invalidateSQLSummaryGeneration(fileID string, generation uint64) {
	s.sqlMu.Lock()
	if cached, ok := s.sqlSummary[fileID]; ok && cached.Generation == generation {
		delete(s.sqlSummary, fileID)
	}
	s.sqlMu.Unlock()
}

func (s *FileService) evictSQLSummariesLocked(keepFileID string) {
	for len(s.sqlSummary) > maxCachedSQLSummaries {
		oldestID := ""
		var oldestUse uint64
		for fileID, cached := range s.sqlSummary {
			if fileID == keepFileID {
				continue
			}
			if oldestID == "" || cached.LastUsed < oldestUse {
				oldestID = fileID
				oldestUse = cached.LastUsed
			}
		}
		if oldestID == "" {
			return
		}
		delete(s.sqlSummary, oldestID)
	}
}

// SqlAnalyze streams a single pass over the dump to discover tables and stats,
// caching the result for later extraction. This is a full pass — the UI should
// show progress for huge files.
func (s *FileService) SqlAnalyze(fileID string) (SqlSummaryResult, error) {
	// Register the cancellable job before acquiring a document lease. Close and
	// refresh therefore never wait behind an analysis that is invisible to the
	// job manager.
	return withFileJobResult(s, fileID, "Analyze SQL dump", jobKindSQLAnalysis, func(ctx context.Context, progress func(int64, string)) (SqlSummaryResult, error) {
		f, ok := s.reg.Get(fileID)
		if !ok {
			return SqlSummaryResult{}, fmt.Errorf("unknown file id %q", fileID)
		}
		defer f.Release()
		fail := func(err error) (SqlSummaryResult, error) {
			s.invalidateSQLSummaryGeneration(fileID, f.Generation)
			if errors.Is(err, ErrOutputSourceChanged) {
				err = errors.Join(ErrSQLAnalysisStale, err)
			}
			return SqlSummaryResult{}, err
		}

		expected, err := captureDocumentSourceExpectation(ctx, f.Doc, f.Path, func(done, _ int64) {
			progress(done, "bytes verified")
		})
		if err != nil {
			return fail(err)
		}
		verifiedSource, err := newVerifiedDocumentReader(ctx, expected, f.Doc)
		if err != nil {
			return fail(err)
		}
		summary, err := sqlanalyze.Analyze(ctx, verifiedSource, sqlanalyze.Options{
			Progress: func(p sqlanalyze.Progress) { progress(p.BytesProcessed, "bytes analyzed") },
		})
		if err != nil {
			return fail(err)
		}
		if err := expected.validateContext(ctx, f.Doc, func(done, _ int64) {
			progress(done, "bytes reverified")
		}); err != nil {
			return fail(err)
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}

		// Per-table byte size is the sum of exact owned statement regions.
		sizeByName := map[string]int64{}
		if ranges, planErr := sqlextract.PlanTableRanges(summary, f.Doc.Size(), sqlextract.PlanOptions{}); planErr == nil {
			for _, tableRange := range ranges {
				sizeByName[tableRange.Name] = tableRange.Bytes
			}
		}
		tables := make([]SqlTable, 0, len(summary.Tables))
		for _, table := range summary.Tables {
			tables = append(tables, SqlTable{
				Name:         table.Name,
				CreateOffset: table.CreateOffset,
				InsertOffset: table.InsertOffset,
				Bytes:        sizeByName[table.Name],
			})
		}
		result := SqlSummaryResult{
			Tables:       tables,
			CreateTables: summary.CreateTables,
			InsertTables: summary.InsertTables,
			DefinerCount: summary.DefinerCount,
			Header:       summary.MysqldumpHeader,
		}
		if err := publishServiceJobOutput(ctx, func() (bool, error) {
			s.sqlMu.Lock()
			s.sqlUseSeq++
			s.sqlSummary[fileID] = cachedSQLSummary{
				Generation: f.Generation,
				Summary:    summary,
				Digest:     expected.digest,
				Verified:   true,
				LastUsed:   s.sqlUseSeq,
			}
			s.evictSQLSummariesLocked(fileID)
			s.sqlMu.Unlock()
			return true, nil
		}); err != nil {
			return fail(err)
		}
		return result, nil
	})
}

// SqlExtractTableViaDialog writes one table's exact analyzed
// CREATE/INSERT/REPLACE regions to a chosen output file. Surrounding session
// and unrelated SQL is deliberately omitted.
func (s *FileService) SqlExtractTableViaDialog(fileID string, tableName string) (TransformResult, error) {
	if err := validateSQLTableSelection(tableName, false); err != nil {
		return TransformResult{}, err
	}
	preflight, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	generation := preflight.Generation
	planned, err := sqlextract.PlanExtractTable(summary, preflight.Doc.Size(), tableName, sqlextract.PlanOptions{})
	preflight.Release()
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := sqlSaveDialog("Extract table to", safeFileName(tableName)+".sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	written, err := withFileJobResult(s, fileID, "Extract SQL table", jobKindTransform, func(ctx context.Context, progress func(int64, string)) (int64, error) {
		current, currentSummary, err := s.sqlSummaryForGeneration(fileID, generation)
		if err != nil {
			return 0, sqlAnalysisAfterDialogError(err)
		}
		defer current.Release()
		currentRange, err := sqlextract.PlanExtractTable(currentSummary, current.Doc.Size(), tableName, sqlextract.PlanOptions{})
		if err != nil {
			return 0, err
		}
		expected, err := captureDocumentSourceExpectation(ctx, current.Doc, current.Path, nil)
		if err != nil {
			return 0, err
		}
		verifiedSource, err := newVerifiedDocumentReader(ctx, expected, current.Doc)
		if err != nil {
			return 0, err
		}
		return exportRangesValidatedReader(ctx, current.Doc, verifiedSource, current.Path, dst, sqlextract.ByteRanges(currentRange), progress, func(validateCtx context.Context) error {
			return expected.validateContext(validateCtx, current.Doc, nil)
		})
	})
	result := TransformResult{
		OutputPath:     dst,
		RecordsWritten: int64(len(sqlextract.ByteRanges(planned))),
		Note:           fmt.Sprintf("%s — analyzed regions only — %s", tableName, fmtByteCount(written)),
	}
	if err != nil {
		return transformResultAfterPublication(result, err)
	}
	return result, nil
}

// SqlLintFinding is one issue found in a dump.
type SqlLintFinding struct {
	Severity string `json:"severity"` // info | warn
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

// SqlLintResult is the dump-linter output (derived from the cached analysis).
type SqlLintResult struct {
	Findings []SqlLintFinding `json:"findings"`
}

// SqlLint reports dump issues from the cached analysis: empty tables, DEFINER
// usage, mixed charsets/collations, and the largest tables. No new file scan.
func (s *FileService) SqlLint(fileID string) (SqlLintResult, error) {
	f, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return SqlLintResult{}, err
	}
	defer f.Release()
	var out []SqlLintFinding

	empty := make([]string, 0)
	for _, t := range summary.Tables {
		if t.CreateOffset >= 0 && t.InsertOffset < 0 {
			empty = append(empty, t.Name)
		}
	}
	if len(empty) > 0 {
		out = append(out, SqlLintFinding{Severity: "info", Title: fmt.Sprintf("%d empty tables", len(empty)), Detail: previewList(empty)})
	}
	if summary.DefinerCount > 0 {
		out = append(out, SqlLintFinding{Severity: "warn", Title: fmt.Sprintf("%d DEFINER clauses", summary.DefinerCount), Detail: "Re-import may fail unless the definer user exists; review these clauses with a token-aware SQL tool."})
	}
	if len(summary.Charsets) > 1 {
		out = append(out, SqlLintFinding{Severity: "warn", Title: "mixed charsets", Detail: mapKeys(summary.Charsets)})
	}
	if len(summary.Collations) > 1 {
		out = append(out, SqlLintFinding{Severity: "info", Title: "mixed collations", Detail: mapKeys(summary.Collations)})
	}
	if ranges, perr := sqlextract.PlanTableRanges(summary, f.Doc.Size(), sqlextract.PlanOptions{}); perr == nil {
		largest := append([]sqlextract.TableRange(nil), ranges...)
		sort.Slice(largest, func(i, j int) bool { return largest[i].Bytes > largest[j].Bytes })
		parts := make([]string, 0, 3)
		for i := 0; i < len(largest) && i < 3; i++ {
			parts = append(parts, fmt.Sprintf("%s (%s)", largest[i].Name, fmtByteCount(largest[i].Bytes)))
		}
		if len(parts) > 0 {
			out = append(out, SqlLintFinding{Severity: "info", Title: "largest tables", Detail: strings.Join(parts, ", ")})
		}
	}
	if len(out) == 0 {
		out = append(out, SqlLintFinding{Severity: "info", Title: "no issues found", Detail: ""})
	}
	return SqlLintResult{Findings: out}, nil
}

func previewList(names []string) string {
	if len(names) > 8 {
		return strings.Join(names[:8], ", ") + fmt.Sprintf(", … (+%d)", len(names)-8)
	}
	return strings.Join(names, ", ")
}

func mapKeys(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// SqlSplitByTableViaDialog writes one .sql file per table into a chosen folder.
func (s *FileService) SqlSplitByTableViaDialog(fileID string) (TransformResult, error) {
	preflight, _, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	generation := preflight.Generation
	preflight.Release()
	dir, err := sqlDirDialog("Choose a folder for the per-table files")
	if err != nil || strings.TrimSpace(dir) == "" {
		return TransformResult{}, err
	}
	sum, err := withFileJobResult(s, fileID, "Split SQL dump", jobKindTransform, func(ctx context.Context, progress func(int64, string)) (sqlextract.WriteSummary, error) {
		current, summary, err := s.sqlSummaryForGeneration(fileID, generation)
		if err != nil {
			return sqlextract.WriteSummary{}, sqlAnalysisAfterDialogError(err)
		}
		defer current.Release()
		expected, err := captureDocumentSourceExpectation(ctx, current.Doc, current.Path, nil)
		if err != nil {
			return sqlextract.WriteSummary{}, err
		}
		verifiedSource, err := newVerifiedDocumentReader(ctx, expected, current.Doc)
		if err != nil {
			return sqlextract.WriteSummary{}, err
		}
		validateState := func(validateCtx context.Context) error {
			info, state, err := inspectDocumentSourceIdentity(current.Doc, current.Path, expected.info)
			if err != nil {
				return err
			}
			if !sameFileState(expected.info, info) || !expected.state.Equal(state) {
				return ErrOutputSourceChanged
			}
			return validateCtx.Err()
		}
		return sqlextract.SplitByTable(ctx, verifiedSource, current.Path, summary, sqlextract.WriteOptions{
			PlanOptions:    sqlextract.PlanOptions{OutputDir: dir},
			Progress:       func(done int64, _ int64, _ int) { progress(done, "bytes written") },
			ValidateSource: validateState,
			ValidateCompletion: func(validateCtx context.Context) error {
				return expected.validateContext(validateCtx, current.Doc, nil)
			},
		})
	})
	result := TransformResult{
		OutputPath:     dir,
		RecordsWritten: int64(len(sum.Outputs)),
		Note:           fmt.Sprintf("%d tables · analyzed regions only · %s total", len(sum.Outputs), fmtByteCount(sum.BytesWritten)),
	}
	if err == nil {
		return result, nil
	}
	if len(sum.Outputs) == 0 {
		return TransformResult{}, err
	}
	if sum.Complete {
		result.Note += "; completion manifest published with a finalization warning"
	} else {
		result.Note += "; complete table outputs retained from an incomplete split"
	}
	return result, err
}

// SqlExtractSchemaViaDialog writes exact analyzed CREATE regions with no INSERT
// data. An empty tableName selects every discovered table.
func (s *FileService) SqlExtractSchemaViaDialog(fileID, tableName string) (TransformResult, error) {
	if err := validateSQLTableSelection(tableName, true); err != nil {
		return TransformResult{}, err
	}
	preflight, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	generation := preflight.Generation
	plannedRegions, defName, err := planSchemaExtraction(summary, preflight.Doc.Size(), tableName)
	preflight.Release()
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := sqlSaveDialog("Save schema as", defName)
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	written, err := withFileJobResult(s, fileID, "Extract SQL schema", jobKindTransform, func(ctx context.Context, progress func(int64, string)) (int64, error) {
		current, currentSummary, err := s.sqlSummaryForGeneration(fileID, generation)
		if err != nil {
			return 0, sqlAnalysisAfterDialogError(err)
		}
		defer current.Release()
		regions, _, err := planSchemaExtraction(currentSummary, current.Doc.Size(), tableName)
		if err != nil {
			return 0, err
		}
		expected, err := captureDocumentSourceExpectation(ctx, current.Doc, current.Path, nil)
		if err != nil {
			return 0, err
		}
		verifiedSource, err := newVerifiedDocumentReader(ctx, expected, current.Doc)
		if err != nil {
			return 0, err
		}
		return exportRangesValidatedReader(ctx, current.Doc, verifiedSource, current.Path, dst, regions, progress, func(validateCtx context.Context) error {
			return expected.validateContext(validateCtx, current.Doc, nil)
		})
	})
	result := TransformResult{OutputPath: dst, RecordsWritten: int64(len(plannedRegions)), Note: "CREATE regions only — " + fmtByteCount(written)}
	if err != nil {
		return transformResultAfterPublication(result, err)
	}
	return result, nil
}

// SqlExtractDataViaDialog writes exact analyzed INSERT/REPLACE regions for one
// table, with no DDL or surrounding dump SQL.
func (s *FileService) SqlExtractDataViaDialog(fileID, tableName string) (TransformResult, error) {
	if err := validateSQLTableSelection(tableName, false); err != nil {
		return TransformResult{}, err
	}
	preflight, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	generation := preflight.Generation
	plannedRegions, err := planDataExtraction(summary, preflight.Doc.Size(), tableName)
	preflight.Release()
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := sqlSaveDialog("Save data as", safeFileName(tableName)+".data.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	written, err := withFileJobResult(s, fileID, "Extract SQL data", jobKindTransform, func(ctx context.Context, progress func(int64, string)) (int64, error) {
		current, currentSummary, err := s.sqlSummaryForGeneration(fileID, generation)
		if err != nil {
			return 0, sqlAnalysisAfterDialogError(err)
		}
		defer current.Release()
		regions, err := planDataExtraction(currentSummary, current.Doc.Size(), tableName)
		if err != nil {
			return 0, err
		}
		expected, err := captureDocumentSourceExpectation(ctx, current.Doc, current.Path, nil)
		if err != nil {
			return 0, err
		}
		verifiedSource, err := newVerifiedDocumentReader(ctx, expected, current.Doc)
		if err != nil {
			return 0, err
		}
		return exportRangesValidatedReader(ctx, current.Doc, verifiedSource, current.Path, dst, regions, progress, func(validateCtx context.Context) error {
			return expected.validateContext(validateCtx, current.Doc, nil)
		})
	})
	result := TransformResult{OutputPath: dst, RecordsWritten: int64(len(plannedRegions)), Note: "INSERT/REPLACE regions only — " + fmtByteCount(written)}
	if err != nil {
		return transformResultAfterPublication(result, err)
	}
	return result, nil
}

// sqlSummaryFor returns the file and its cached analysis, or an error to analyze first.
func (s *FileService) sqlSummaryFor(fileID string) (*session.File, sqlanalyze.Summary, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return nil, sqlanalyze.Summary{}, fmt.Errorf("unknown file id %q", fileID)
	}
	s.sqlMu.Lock()
	cached, have := s.sqlSummary[fileID]
	if have && cached.Generation == f.Generation {
		s.sqlUseSeq++
		cached.LastUsed = s.sqlUseSeq
		s.sqlSummary[fileID] = cached
	}
	s.sqlMu.Unlock()
	if !have || cached.Generation != f.Generation {
		f.Release()
		return nil, sqlanalyze.Summary{}, ErrSQLAnalysisRequired
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		generation := f.Generation
		f.Release()
		s.invalidateSQLSummaryGeneration(fileID, generation)
		return nil, sqlanalyze.Summary{}, errors.Join(ErrSQLAnalysisStale, err)
	}
	return f, cached.Summary, nil
}

// sqlSummaryForGeneration reacquires both the retained document and analysis
// snapshot after a native dialog. A refreshed document is reported as a source
// change even though RefreshFile also invalidates its cached SQL summary.
func (s *FileService) sqlSummaryForGeneration(fileID string, generation uint64) (*session.File, sqlanalyze.Summary, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return nil, sqlanalyze.Summary{}, fmt.Errorf("unknown file id %q", fileID)
	}
	if f.Generation != generation {
		f.Release()
		return nil, sqlanalyze.Summary{}, errors.Join(ErrSQLAnalysisStale, ErrOutputSourceChanged)
	}
	s.sqlMu.Lock()
	cached, have := s.sqlSummary[fileID]
	if have && cached.Generation == generation {
		s.sqlUseSeq++
		cached.LastUsed = s.sqlUseSeq
		s.sqlSummary[fileID] = cached
	}
	s.sqlMu.Unlock()
	if !have || cached.Generation != generation {
		f.Release()
		return nil, sqlanalyze.Summary{}, ErrSQLAnalysisRequired
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		f.Release()
		s.invalidateSQLSummaryGeneration(fileID, generation)
		return nil, sqlanalyze.Summary{}, errors.Join(ErrSQLAnalysisStale, err)
	}
	return f, cached.Summary, nil
}

func findRange(ranges []sqlextract.TableRange, name string) (sqlextract.TableRange, bool) {
	for _, r := range ranges {
		if r.Name == name {
			return r, true
		}
	}
	return sqlextract.TableRange{}, false
}

// schemaRegion is a table's DDL byte range (CREATE … up to the first INSERT).
func schemaRegions(r sqlextract.TableRange) [][2]int64 {
	if len(r.Regions) > 0 {
		regions := make([][2]int64, 0, len(r.Regions))
		for _, region := range r.Regions {
			if region.Kind == sqlanalyze.RegionCreate {
				regions = append(regions, [2]int64{region.StartOffset, region.EndOffset})
			}
		}
		return regions
	}
	if r.CreateOffset < r.StartOffset || r.CreateOffset < 0 || r.CreateOffset >= r.EndOffset {
		return nil
	}
	start := r.CreateOffset
	end := r.EndOffset
	if r.InsertOffset > start && r.InsertOffset <= r.EndOffset {
		end = r.InsertOffset
	}
	if end <= start {
		return nil
	}
	return [][2]int64{{start, end}}
}

// dataRegion is a table's INSERT byte range (first INSERT to the next table).
func dataRegions(r sqlextract.TableRange) [][2]int64 {
	if len(r.Regions) > 0 {
		regions := make([][2]int64, 0, len(r.Regions))
		for _, region := range r.Regions {
			if region.Kind == sqlanalyze.RegionInsert || region.Kind == sqlanalyze.RegionReplace {
				regions = append(regions, [2]int64{region.StartOffset, region.EndOffset})
			}
		}
		return regions
	}
	if r.InsertOffset < r.StartOffset || r.InsertOffset < 0 || r.InsertOffset >= r.EndOffset {
		return nil
	}
	return [][2]int64{{r.InsertOffset, r.EndOffset}}
}

func planSchemaExtraction(summary sqlanalyze.Summary, sourceSize int64, tableName string) ([][2]int64, string, error) {
	ranges, err := sqlextract.PlanTableRanges(summary, sourceSize, sqlextract.PlanOptions{})
	if err != nil {
		return nil, "", err
	}
	regions := make([][2]int64, 0, len(ranges))
	defaultName := "schema.sql"
	if strings.TrimSpace(tableName) == "" {
		schemaCount := 0
		for _, tableRange := range ranges {
			owned := schemaRegions(tableRange)
			regions = append(regions, owned...)
			schemaCount += len(owned)
		}
		if schemaCount == 0 {
			return nil, "", errors.New("no CREATE TABLE schema was discovered")
		}
		sort.SliceStable(regions, func(i, j int) bool { return regions[i][0] < regions[j][0] })
		return regions, defaultName, nil
	}
	tableRange, ok := findRange(ranges, tableName)
	if !ok {
		return nil, "", fmt.Errorf("table %q was not discovered", tableName)
	}
	owned := schemaRegions(tableRange)
	if len(owned) == 0 {
		return nil, "", fmt.Errorf("table %q has no CREATE TABLE schema", tableName)
	}
	return owned, safeFileName(tableName) + ".schema.sql", nil
}

func planDataExtraction(summary sqlanalyze.Summary, sourceSize int64, tableName string) ([][2]int64, error) {
	ranges, err := sqlextract.PlanTableRanges(summary, sourceSize, sqlextract.PlanOptions{})
	if err != nil {
		return nil, err
	}
	tableRange, ok := findRange(ranges, tableName)
	if !ok {
		return nil, fmt.Errorf("table %q was not discovered", tableName)
	}
	regions := dataRegions(tableRange)
	if len(regions) == 0 {
		return nil, fmt.Errorf("table %q has no INSERT or REPLACE data", tableName)
	}
	return regions, nil
}

func validateSQLTableSelection(tableName string, allowEmpty bool) error {
	if len(tableName) > sqlextract.MaxTableSelectionBytes {
		return sqlextract.ErrTableNameTooLong
	}
	if !allowEmpty && strings.TrimSpace(tableName) == "" {
		return errors.New("table name is required")
	}
	return nil
}

func transformResultAfterPublication(result TransformResult, err error) (TransformResult, error) {
	if err == nil {
		return result, nil
	}
	var publication *fileio.PublicationError
	if errors.As(err, &publication) &&
		!publication.LocationUncertain &&
		publication.FinalPath == result.OutputPath {
		if result.Note != "" {
			result.Note += "; "
		}
		result.Note += "output published with a finalization warning"
		return result, err
	}
	return TransformResult{}, err
}

// exportRanges streams the given byte ranges of doc, in order, into one file.
func exportRanges(ctx context.Context, doc *document.FileDocument, sourcePath, dst string, ranges [][2]int64, progress func(int64, string)) (int64, error) {
	return exportRangesValidated(ctx, doc, sourcePath, dst, ranges, progress, nil)
}

// exportRangesValidated stages all selected ranges and publishes them only
// after the caller confirms that the retained source still matches the exact
// source snapshot used to build the range plan.
func exportRangesValidated(ctx context.Context, doc *document.FileDocument, sourcePath, dst string, ranges [][2]int64, progress func(int64, string), validate func(context.Context) error) (int64, error) {
	return exportRangesValidatedReader(ctx, doc, doc, sourcePath, dst, ranges, progress, validate)
}

func exportRangesValidatedReader(ctx context.Context, doc *document.FileDocument, source io.ReaderAt, sourcePath, dst string, ranges [][2]int64, progress func(int64, string), validate func(context.Context) error) (int64, error) {
	if source == nil {
		return 0, errors.New("SQL export source is required")
	}
	return writeSafeOutputValidatedContext(ctx, doc, sourcePath, dst, func(out io.Writer) error {
		bw := bufio.NewWriterSize(out, 1<<20)
		var written int64
		for _, rg := range ranges {
			if err := ctx.Err(); err != nil {
				return err
			}
			if rg[1] <= rg[0] {
				continue
			}
			n, err := io.Copy(bw, &jobContextReader{ctx: ctx, reader: io.NewSectionReader(source, rg[0], rg[1]-rg[0])})
			written += n
			if progress != nil {
				progress(written, "bytes written")
			}
			if err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return bw.Flush()
	}, validate)
}

type jobContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *jobContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// SqlSampleFixtureViaDialog writes a small "dev fixture" dump: each table's DDL
// plus only its first rowsPerTable INSERT rows. Turns a prod dump into a tiny,
// shareable seed without ever materialising the whole file.
func (s *FileService) SqlSampleFixtureViaDialog(fileID string, rowsPerTable int) (TransformResult, error) {
	preflight, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	generation := preflight.Generation
	rowsPerTable = clampRequestInt(rowsPerTable, defaultSQLFixtureRows, maxSQLFixtureRows)
	plannedRanges, err := sqlextract.PlanTableRanges(summary, preflight.Doc.Size(), sqlextract.PlanOptions{})
	preflight.Release()
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := sqlSaveDialog("Save dev fixture as", "fixture.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}

	size, err := withFileJobResult(s, fileID, "Create SQL fixture", jobKindTransform, func(ctx context.Context, progress func(int64, string)) (int64, error) {
		current, currentSummary, err := s.sqlSummaryForGeneration(fileID, generation)
		if err != nil {
			return 0, sqlAnalysisAfterDialogError(err)
		}
		defer current.Release()
		ranges, err := sqlextract.PlanTableRanges(currentSummary, current.Doc.Size(), sqlextract.PlanOptions{})
		if err != nil {
			return 0, err
		}
		expected, err := captureDocumentSourceExpectation(ctx, current.Doc, current.Path, nil)
		if err != nil {
			return 0, err
		}
		verifiedSource, err := newVerifiedDocumentReader(ctx, expected, current.Doc)
		if err != nil {
			return 0, err
		}
		return writeSafeOutputValidatedContext(ctx, current.Doc, current.Path, dst, func(out io.Writer) error {
			bw := bufio.NewWriterSize(out, 1<<20)
			var processed int64

			// Preserve the leading session preamble so the fixture can be imported
			// with the source dump's charset and session setup.
			if len(ranges) > 0 && ranges[0].StartOffset > 0 {
				n, err := copyRange(ctx, bw, verifiedSource, 0, ranges[0].StartOffset)
				processed += n
				progress(processed, "bytes read")
				if err != nil {
					return err
				}
			}
			for _, tableRange := range ranges {
				for _, schema := range schemaRegions(tableRange) {
					n, err := copyRange(ctx, bw, verifiedSource, schema[0], schema[1])
					processed += n
					progress(processed, "bytes read")
					if err != nil {
						return err
					}
				}
				remaining := rowsPerTable
				for _, data := range dataRegions(tableRange) {
					if remaining <= 0 {
						break
					}
					for _, owned := range tableRange.Regions {
						if owned.StartOffset == data[0] && owned.EndOffset == data[1] && owned.Kind == sqlanalyze.RegionReplace {
							return fmt.Errorf("table %q uses REPLACE; bounded fixture row sampling supports strict INSERT ... VALUES only", tableRange.Name)
						}
					}
					sample, sampledRows, read, err := sampleInsertRowsCount(ctx, verifiedSource, data[0], data[1], remaining)
					processed += read
					progress(processed, "bytes read")
					if err != nil {
						return err
					}
					if _, err := bw.Write(sample); err != nil {
						return err
					}
					remaining -= sampledRows
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return bw.Flush()
		}, func(validateCtx context.Context) error {
			return expected.validateContext(validateCtx, current.Doc, nil)
		})
	})
	result := TransformResult{
		OutputPath:     dst,
		RecordsWritten: int64(len(plannedRanges)),
		Note:           fmt.Sprintf("%d tables · ≤%d rows each · %s", len(plannedRanges), rowsPerTable, fmtByteCount(size)),
	}
	if err != nil {
		return transformResultAfterPublication(result, err)
	}
	return result, nil
}

// copyRange streams doc[start:end) into bw and rejects a short source.
func copyRange(ctx context.Context, bw *bufio.Writer, doc document.ReaderAtSize, start, end int64) (int64, error) {
	if doc == nil {
		return 0, errors.New("document is required")
	}
	if start < 0 || end < start || end > doc.Size() {
		return 0, fmt.Errorf("invalid copy range [%d,%d) for document size %d", start, end, doc.Size())
	}
	if end <= start {
		return 0, nil
	}
	n, err := io.Copy(bw, &jobContextReader{ctx: ctx, reader: io.NewSectionReader(doc, start, end-start)})
	if err == nil && n != end-start {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

const maxFixtureSampleBytes int64 = 8 << 20

// sampleInsertRows returns a strict, syntactically complete INSERT prefix from
// doc[start:end). The bounded materialized prefix is parsed by the same grammar
// and safety guard used by SQL reshape; guessed parenthesis counting is not
// sufficient because column lists, nested expressions, comments, and trailing
// clauses all affect whether a fixture is safe to publish.
func sampleInsertRows(ctx context.Context, r io.ReaderAt, start, end int64, maxRows int) ([]byte, int64, error) {
	sample, _, read, err := sampleInsertRowsCount(ctx, r, start, end, maxRows)
	return sample, read, err
}

func sampleInsertRowsCount(ctx context.Context, r io.ReaderAt, start, end int64, maxRows int) ([]byte, int, int64, error) {
	if r == nil {
		return nil, 0, 0, errors.New("INSERT sample reader is required")
	}
	if start < 0 || end < start {
		return nil, 0, 0, fmt.Errorf("invalid INSERT sample range [%d,%d)", start, end)
	}
	if maxRows <= 0 || maxRows > maxSQLFixtureRows {
		return nil, 0, 0, fmt.Errorf("sample row limit %d is outside 1..%d", maxRows, maxSQLFixtureRows)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}

	readLength := end - start
	truncated := false
	if readLength > maxFixtureSampleBytes {
		readLength = maxFixtureSampleBytes
		truncated = true
	}
	data := make([]byte, int(readLength))
	n, err := r.ReadAt(data, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, int64(n), err
	}
	if n != len(data) {
		return nil, 0, int64(n), io.ErrUnexpectedEOF
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, int64(n), err
	}

	sample, rows, satisfied, err := sqlreshape.SampleInsertRows(data, maxRows)
	if err != nil {
		if truncated && errors.Is(err, sqlreshape.ErrIncompleteStatement) {
			return nil, 0, int64(n), fmt.Errorf("sampled SQL row prefix exceeds %d-byte memory limit", maxFixtureSampleBytes)
		}
		return nil, 0, int64(n), err
	}
	if truncated && !satisfied {
		return nil, 0, int64(n), fmt.Errorf("sampled SQL row prefix exceeds %d-byte memory limit", maxFixtureSampleBytes)
	}
	if rows == 0 {
		return nil, 0, int64(n), errors.New("INSERT sample contains no complete VALUES tuples")
	}
	return sample, rows, int64(n), nil
}

// SqlReplaceViaDialog streams a serialization-aware plain replacement to a new file.
// It updates decoded single-quoted SQL values only, recalculates native
// PHP/WordPress serialized lengths, and never modifies the source.
type sqlReplaceJobSummary struct {
	Matches      int64
	BytesWritten int64
}

func (s *FileService) SqlReplaceViaDialog(fileID, find, replaceWith string, regex, caseInsensitive, wholeWord bool) (TransformResult, error) {
	if regex {
		return TransformResult{}, ErrSQLRegexReplaceUnsupported
	}
	if strings.TrimSpace(find) == "" {
		return TransformResult{}, errors.New("enter text to find")
	}
	if len(find) > replace.MaxSQLReplacePatternBytes {
		return TransformResult{}, fmt.Errorf("find text has %d bytes, maximum is %d", len(find), replace.MaxSQLReplacePatternBytes)
	}
	if len(replaceWith) > replace.MaxSQLReplaceReplacementBytes {
		return TransformResult{}, fmt.Errorf("replacement text has %d bytes, maximum is %d", len(replaceWith), replace.MaxSQLReplaceReplacementBytes)
	}

	preflight, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	generation := preflight.Generation
	if err := validateSQLReplaceCapability(preflight); err != nil {
		preflight.Release()
		return TransformResult{}, err
	}
	preflight.Release()

	dst, err := sqlSaveDialog("Save replaced SQL copy as", "replaced.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}

	summary, err := withFileJobResult(s, fileID, "Replace SQL text", jobKindTransform, func(ctx context.Context, progress func(int64, string)) (sqlReplaceJobSummary, error) {
		current, ok := s.reg.Get(fileID)
		if !ok {
			return sqlReplaceJobSummary{}, fmt.Errorf("unknown file id %q", fileID)
		}
		defer current.Release()
		if current.Generation != generation {
			return sqlReplaceJobSummary{}, ErrOutputSourceChanged
		}
		if err := validateSQLReplaceCapability(current); err != nil {
			return sqlReplaceJobSummary{}, err
		}

		expected, err := captureDocumentSourceExpectation(ctx, current.Doc, current.Path, func(completed, _ int64) {
			progress(completed, "verifying source")
		})
		if err != nil {
			return sqlReplaceJobSummary{}, err
		}
		verifiedSource, err := newVerifiedDocumentReader(ctx, expected, current.Doc)
		if err != nil {
			return sqlReplaceJobSummary{}, err
		}

		var matches int64
		streamVerified := false
		written, err := writeSafeOutputValidatedContext(ctx, current.Doc, current.Path, dst, func(output io.Writer) error {
			streamDigest := sha256.New()
			source := io.TeeReader(io.NewSectionReader(verifiedSource, 0, expected.size), streamDigest)
			matches, err = replace.ReplaceSQLPlain(ctx, source, output, expected.size, []byte(find), []byte(replaceWith), replace.BatchOptions{
				CaseInsensitive: caseInsensitive,
				WholeWord:       wholeWord,
				Progress: func(p replace.Progress) {
					progress(p.BytesProcessed, fmt.Sprintf("%d replacements", p.Matches))
				},
			})
			if err != nil {
				return err
			}
			var got [sha256.Size]byte
			copy(got[:], streamDigest.Sum(nil))
			if got != expected.digest {
				return ErrOutputSourceChanged
			}
			streamVerified = true
			return nil
		}, func(ctx context.Context) error {
			if !streamVerified {
				return ErrOutputSourceChanged
			}
			return expected.validateContext(ctx, current.Doc, func(completed, _ int64) {
				progress(completed, "revalidating source")
			})
		})
		if err != nil {
			return sqlReplaceJobSummary{Matches: matches, BytesWritten: written}, err
		}
		return sqlReplaceJobSummary{Matches: matches, BytesWritten: written}, nil
	})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{
		OutputPath:     dst,
		RecordsWritten: summary.Matches,
		Note:           fmt.Sprintf("%d replacements; PHP/WordPress serialized byte lengths recalculated where applicable", summary.Matches),
	}, nil
}

func validateSQLReplaceCapability(file *session.File) error {
	if file == nil || file.Doc == nil {
		return errors.New("SQL source is required")
	}
	metadata := file.Doc.Metadata()
	if metadata.Binary || !strings.EqualFold(metadata.Encoding, "UTF-8") || !strings.EqualFold(metadata.FileType, "SQL") {
		return fmt.Errorf("%w (detected %s, encoding %s)", ErrSQLReplaceEncodingUnsupported, metadata.FileType, metadata.Encoding)
	}
	return nil
}

// SqlReshapeInsertsViaDialog rewrites INSERT row layout, streaming to a new file.
// mode "single" explodes extended INSERTs into one row each (good for line diffs);
// mode "multi" batches consecutive single-row INSERTs (good for fast re-import).
func (s *FileService) SqlReshapeInsertsViaDialog(fileID, mode string, batchSize int) (TransformResult, error) {
	batchSize = clampRequestInt(batchSize, defaultSQLBatchRows, maxSQLBatchRows)
	var m sqlreshape.Mode
	var defName string
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", string(sqlreshape.ModeSingleRow):
		m = sqlreshape.ModeSingleRow
		defName = "single-row.sql"
	case string(sqlreshape.ModeMultiRow):
		m = sqlreshape.ModeMultiRow
		defName = "batched.sql"
	default:
		return TransformResult{}, fmt.Errorf("unsupported reshape mode %q", mode)
	}
	preflight, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	generation := preflight.Generation
	preflight.Release()

	dst, err := sqlSaveDialog("Save reshaped SQL as", defName)
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	sum, err := withFileJobResult(s, fileID, "Reshape SQL inserts", jobKindTransform, func(ctx context.Context, _ func(int64, string)) (sqlreshape.Summary, error) {
		current, ok := s.reg.Get(fileID)
		if !ok {
			return sqlreshape.Summary{}, fmt.Errorf("unknown file id %q", fileID)
		}
		defer current.Release()
		if current.Generation != generation {
			return sqlreshape.Summary{}, ErrOutputSourceChanged
		}
		expected, err := captureDocumentSourceExpectation(ctx, current.Doc, current.Path, nil)
		if err != nil {
			return sqlreshape.Summary{}, err
		}
		verifiedSource, err := newVerifiedDocumentReader(ctx, expected, current.Doc)
		if err != nil {
			return sqlreshape.Summary{}, err
		}
		var summary sqlreshape.Summary
		_, err = writeSafeOutputValidatedContext(ctx, current.Doc, current.Path, dst, func(output io.Writer) error {
			digest := sha256.New()
			source := io.TeeReader(io.NewSectionReader(verifiedSource, 0, expected.size), digest)
			summary, err = sqlreshape.ReshapeInserts(ctx, source, output, sqlreshape.Options{
				Mode:      m,
				BatchSize: batchSize,
			})
			if err != nil {
				return err
			}
			var consumed [sha256.Size]byte
			copy(consumed[:], digest.Sum(nil))
			if consumed != expected.digest {
				return ErrOutputSourceChanged
			}
			return nil
		}, func(validateCtx context.Context) error {
			return expected.validateContext(validateCtx, current.Doc, nil)
		})
		return summary, err
	})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{
		OutputPath:     dst,
		RecordsRead:    sum.StatementsRead,
		RecordsWritten: sum.StatementsWritten,
		Note:           sqlreshape.FormatNote(m, sum),
	}, nil
}

// SqlSchemaDiffResult is the structural diff between two dumps.
type SqlSchemaDiffResult struct {
	FileA          string                    `json:"fileA"`
	FileB          string                    `json:"fileB"`
	AddedTables    []string                  `json:"addedTables"`
	RemovedTables  []string                  `json:"removedTables"`
	ChangedTables  []sqlschemadiff.TableDiff `json:"changedTables"`
	UnchangedCount int                       `json:"unchangedCount"`
}

// SqlSchemaDiff compares complete bounded CREATE TABLE structure for two
// analyzed dumps (A = baseline, B = new), including constraints, indexes,
// options, qualified identity, and column order.
func (s *FileService) SqlSchemaDiff(fileIDA, fileIDB string) (SqlSchemaDiffResult, error) {
	return runServiceJob(s, jobSpec{
		Title:   "Compare SQL schemas",
		Kind:    "sql-schema-diff",
		FileID:  fileIDA,
		FileIDs: []string{fileIDA, fileIDB},
	}, func(ctx context.Context, progress func(int64, int64, string)) (SqlSchemaDiffResult, error) {
		if fileIDA == fileIDB {
			lease, err := s.acquireSQLSchemaLease(fileIDA)
			if err != nil {
				return SqlSchemaDiffResult{}, err
			}
			defer lease.file.Release()
			return s.sqlSchemaDiffFromLeases(ctx, &lease, &lease, true, progress)
		}

		firstID, secondID := fileIDA, fileIDB
		if secondID < firstID {
			firstID, secondID = secondID, firstID
		}
		first, err := s.acquireSQLSchemaLease(firstID)
		if err != nil {
			return SqlSchemaDiffResult{}, err
		}
		defer first.file.Release()
		second, err := s.acquireSQLSchemaLease(secondID)
		if err != nil {
			return SqlSchemaDiffResult{}, err
		}
		defer second.file.Release()

		baseline, current := &first, &second
		if fileIDA != firstID {
			baseline, current = &second, &first
		}
		return s.sqlSchemaDiffFromLeases(ctx, baseline, current, false, progress)
	})
}

type sqlSchemaLease struct {
	file           *session.File
	summary        sqlanalyze.Summary
	analysisDigest [sha256.Size]byte
	plan           sqlSchemaReadPlan
	expected       *documentSourceExpectation
	verified       *verifiedDocumentReader
}

func (s *FileService) acquireSQLSchemaLease(fileID string) (sqlSchemaLease, error) {
	f, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return sqlSchemaLease{}, err
	}
	s.sqlMu.Lock()
	cached, ok := s.sqlSummary[fileID]
	s.sqlMu.Unlock()
	if !ok || cached.Generation != f.Generation || !cached.Verified {
		f.Release()
		return sqlSchemaLease{}, errors.Join(ErrSQLAnalysisStale, ErrSQLAnalysisRequired)
	}
	notifySQLSchemaDiffLease(fileID)
	return sqlSchemaLease{
		file:           f,
		summary:        summary,
		analysisDigest: cached.Digest,
	}, nil
}

func (s *FileService) sqlSchemaDiffFromLeases(
	ctx context.Context,
	baseline *sqlSchemaLease,
	current *sqlSchemaLease,
	sameFile bool,
	progress func(int64, int64, string),
) (SqlSchemaDiffResult, error) {
	if baseline == nil || current == nil || baseline.file == nil || current.file == nil {
		return SqlSchemaDiffResult{}, errors.New("SQL schema diff requires two retained analysis leases")
	}
	planA, err := planSQLSchemaReads(ctx, baseline.summary, baseline.file.Doc.Size())
	if err != nil {
		return SqlSchemaDiffResult{}, err
	}
	baseline.plan = planA
	plans := []sqlSchemaReadPlan{planA}
	if sameFile {
		current.plan = planA
	} else {
		planB, err := planSQLSchemaReads(ctx, current.summary, current.file.Doc.Size())
		if err != nil {
			return SqlSchemaDiffResult{}, err
		}
		current.plan = planB
		plans = append(plans, planB)
	}
	budget, err := newSQLSchemaDiffBudget(plans...)
	if err != nil {
		return SqlSchemaDiffResult{}, err
	}

	sourceBytes := baseline.file.Doc.Size()
	if !sameFile {
		if current.file.Doc.Size() > maxJobProgressValue-sourceBytes {
			sourceBytes = maxJobProgressValue
		} else {
			sourceBytes += current.file.Doc.Size()
		}
	}
	totalWork := sqlSchemaDiffWorkTotal(sourceBytes, budget.totalInput)
	completed := int64(0)
	report := func(value int64, note string) {
		if progress != nil {
			progress(value, totalWork, note)
		}
	}

	if err := s.captureSQLSchemaLease(ctx, baseline, completed, totalWork, "verifying baseline source", progress); err != nil {
		return SqlSchemaDiffResult{}, err
	}
	completed += baseline.file.Doc.Size()
	if sameFile {
		current.expected = baseline.expected
		current.verified = baseline.verified
	} else {
		if err := s.captureSQLSchemaLease(ctx, current, completed, totalWork, "verifying comparison source", progress); err != nil {
			return SqlSchemaDiffResult{}, err
		}
		completed += current.file.Doc.Size()
	}

	parseBase := completed
	ta, err := s.parsedSchemaFromLease(ctx, baseline, budget, func(used int64, note string) {
		report(parseBase+used, note)
	})
	if err != nil {
		return SqlSchemaDiffResult{}, err
	}
	var tb []sqlschemadiff.Table
	if sameFile {
		tb = ta
	} else {
		tb, err = s.parsedSchemaFromLease(ctx, current, budget, func(used int64, note string) {
			report(parseBase+used, note)
		})
		if err != nil {
			return SqlSchemaDiffResult{}, err
		}
	}
	completed = parseBase + budget.totalInput

	diff, err := sqlschemadiff.DiffContext(ctx, ta, tb)
	if err != nil {
		return SqlSchemaDiffResult{}, err
	}
	if err := s.revalidateSQLSchemaLease(ctx, baseline, completed, totalWork, "revalidating baseline source", progress); err != nil {
		return SqlSchemaDiffResult{}, err
	}
	completed += baseline.file.Doc.Size()
	if !sameFile {
		if err := s.revalidateSQLSchemaLease(ctx, current, completed, totalWork, "revalidating comparison source", progress); err != nil {
			return SqlSchemaDiffResult{}, err
		}
		completed += current.file.Doc.Size()
	}
	if !s.reg.IsCurrentGeneration(baseline.file.ID, baseline.file.Generation) ||
		(!sameFile && !s.reg.IsCurrentGeneration(current.file.ID, current.file.Generation)) {
		s.invalidateSQLSummaryGeneration(baseline.file.ID, baseline.file.Generation)
		if !sameFile {
			s.invalidateSQLSummaryGeneration(current.file.ID, current.file.Generation)
		}
		return SqlSchemaDiffResult{}, errors.Join(ErrSQLAnalysisStale, ErrOutputSourceChanged)
	}

	result := SqlSchemaDiffResult{
		FileA:          baseName(baseline.file.Path),
		FileB:          baseName(current.file.Path),
		AddedTables:    diff.AddedTables,
		RemovedTables:  diff.RemovedTables,
		ChangedTables:  diff.ChangedTables,
		UnchangedCount: diff.UnchangedCount,
	}
	if err := commitSQLSchemaDiff(ctx, s, baseline, current, sameFile); err != nil {
		return SqlSchemaDiffResult{}, err
	}
	report(completed, "schemas compared")
	return result, nil
}

func sqlSchemaDiffWorkTotal(sourceBytes, inputBytes int64) int64 {
	if sourceBytes < 0 || inputBytes < 0 || sourceBytes > (maxJobProgressValue-inputBytes)/2 {
		return maxJobProgressValue
	}
	return sourceBytes*2 + inputBytes
}

func (s *FileService) captureSQLSchemaLease(
	ctx context.Context,
	lease *sqlSchemaLease,
	completed int64,
	total int64,
	note string,
	progress func(int64, int64, string),
) error {
	expected, err := captureDocumentSourceExpectation(ctx, lease.file.Doc, lease.file.Path, func(done, _ int64) {
		if progress != nil {
			progress(completed+done, total, note)
		}
	})
	if err != nil {
		return s.sqlSchemaSourceError(lease, err)
	}
	if expected.digest != lease.analysisDigest {
		return s.sqlSchemaSourceError(lease, ErrOutputSourceChanged)
	}
	verified, err := newVerifiedDocumentReader(ctx, expected, lease.file.Doc)
	if err != nil {
		return s.sqlSchemaSourceError(lease, err)
	}
	lease.expected = expected
	lease.verified = verified
	return nil
}

func (s *FileService) revalidateSQLSchemaLease(
	ctx context.Context,
	lease *sqlSchemaLease,
	completed int64,
	total int64,
	note string,
	progress func(int64, int64, string),
) error {
	err := lease.expected.validateContext(ctx, lease.file.Doc, func(done, _ int64) {
		if progress != nil {
			progress(completed+done, total, note)
		}
	})
	return s.sqlSchemaSourceError(lease, err)
}

func (s *FileService) sqlSchemaSourceError(lease *sqlSchemaLease, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrOutputSourceChanged) {
		if lease != nil && lease.file != nil {
			s.invalidateSQLSummaryGeneration(lease.file.ID, lease.file.Generation)
		}
		return errors.Join(ErrSQLAnalysisStale, err)
	}
	return err
}

func commitSQLSchemaDiff(
	ctx context.Context,
	service *FileService,
	baseline *sqlSchemaLease,
	current *sqlSchemaLease,
	sameFile bool,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	jobID, ok := serviceJobID(ctx)
	if !ok {
		return nil
	}
	generationsCurrent := true
	if !service.jobs().commit(jobID, func() bool {
		generationsCurrent = ctx.Err() == nil &&
			baseline != nil &&
			baseline.file != nil &&
			service.reg.IsCurrentGeneration(baseline.file.ID, baseline.file.Generation) &&
			(sameFile ||
				(current != nil &&
					current.file != nil &&
					service.reg.IsCurrentGeneration(current.file.ID, current.file.Generation)))
		return generationsCurrent
	}) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !generationsCurrent {
			service.invalidateSQLSummaryGeneration(baseline.file.ID, baseline.file.Generation)
			if !sameFile && current != nil && current.file != nil {
				service.invalidateSQLSummaryGeneration(current.file.ID, current.file.Generation)
			}
			return errors.Join(ErrSQLAnalysisStale, ErrOutputSourceChanged)
		}
		return ErrJobCancelled
	}
	return ctx.Err()
}

const maxSQLSchemaStatementReadBytes = int64(sqlschemadiff.MaxStatementBytes) + 1

type sqlSchemaRead struct {
	name   string
	start  int64
	end    int64
	hasDDL bool
}

type sqlSchemaReadPlan struct {
	reads      []sqlSchemaRead
	inputBytes int64
}

type sqlSchemaDiffBudget struct {
	inputRemaining    int64
	retainedRemaining int64
	inputUsed         int64
	totalInput        int64
}

// planSQLSchemaReads computes and validates every exact CREATE-region read
// before source fingerprinting or DDL reads begin. Summaries without exact
// analyzer regions are retained as explicit unknown schemas rather than
// falling back to enclosing table/data spans.
func planSQLSchemaReads(ctx context.Context, summary sqlanalyze.Summary, sourceSize int64) (sqlSchemaReadPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return sqlSchemaReadPlan{}, err
	}
	if sourceSize < 0 {
		return sqlSchemaReadPlan{}, errors.New("SQL schema source size must be non-negative")
	}
	if len(summary.Tables) > sqlschemadiff.MaxDiffTableCount {
		return sqlSchemaReadPlan{}, fmt.Errorf(
			"%w: table count %d exceeds per-side limit %d",
			ErrSQLSchemaDiffBudget,
			len(summary.Tables),
			sqlschemadiff.MaxDiffTableCount,
		)
	}

	plan := sqlSchemaReadPlan{reads: make([]sqlSchemaRead, 0, len(summary.Tables))}
	for tableIndex, table := range summary.Tables {
		if tableIndex%64 == 0 {
			if err := ctx.Err(); err != nil {
				return sqlSchemaReadPlan{}, err
			}
		}
		read := sqlSchemaRead{name: table.Name}
		createCount := 0
		var create sqlanalyze.Region
		previousEnd := int64(-1)
		for regionIndex, region := range table.Regions {
			if regionIndex%256 == 0 {
				if err := ctx.Err(); err != nil {
					return sqlSchemaReadPlan{}, err
				}
			}
			if region.StartOffset < 0 || region.EndOffset <= region.StartOffset || region.EndOffset > sourceSize {
				return sqlSchemaReadPlan{}, fmt.Errorf(
					"table %q has invalid %s region [%d,%d)",
					table.Name,
					region.Kind,
					region.StartOffset,
					region.EndOffset,
				)
			}
			if previousEnd > region.StartOffset {
				return sqlSchemaReadPlan{}, fmt.Errorf("table %q has overlapping or unordered SQL regions", table.Name)
			}
			switch region.Kind {
			case sqlanalyze.RegionCreate:
				createCount++
				create = region
			case sqlanalyze.RegionInsert, sqlanalyze.RegionReplace:
			default:
				return sqlSchemaReadPlan{}, fmt.Errorf("table %q has unsupported SQL region kind %q", table.Name, region.Kind)
			}
			previousEnd = region.EndOffset
		}
		if createCount == 1 {
			read.start, read.end, read.hasDDL = create.StartOffset, create.EndOffset, true
			if read.end-read.start > maxSQLSchemaStatementReadBytes {
				read.end = read.start + maxSQLSchemaStatementReadBytes
			}
			readBytes := read.end - read.start
			limit := int64(sqlschemadiff.MaxDiffDefinitionBytes)
			if readBytes < 0 || plan.inputBytes > limit-readBytes {
				plan.inputBytes = limit + 1
			} else {
				plan.inputBytes += readBytes
			}
		}
		plan.reads = append(plan.reads, read)
	}
	if err := ctx.Err(); err != nil {
		return sqlSchemaReadPlan{}, err
	}
	return plan, nil
}

func newSQLSchemaDiffBudget(plans ...sqlSchemaReadPlan) (*sqlSchemaDiffBudget, error) {
	inputAdmission := int64(sqlschemadiff.MaxDiffDefinitionBytes)
	retainedAdmission := int64(sqlschemadiff.MaxDiffDefinitionBytes)
	totalInput := int64(0)
	for _, plan := range plans {
		if plan.inputBytes < 0 || plan.inputBytes > inputAdmission {
			return nil, fmt.Errorf(
				"%w: planned CREATE TABLE input exceeds %d bytes",
				ErrSQLSchemaDiffBudget,
				sqlschemadiff.MaxDiffDefinitionBytes,
			)
		}
		inputAdmission -= plan.inputBytes
		totalInput += plan.inputBytes
		for _, read := range plan.reads {
			minimum := sqlschemadiff.TableRetainedBytes(sqlschemadiff.Table{Name: read.name})
			if minimum < 0 || minimum > retainedAdmission {
				return nil, fmt.Errorf(
					"%w: minimum retained schema state exceeds %d bytes",
					ErrSQLSchemaDiffBudget,
					sqlschemadiff.MaxDiffDefinitionBytes,
				)
			}
			retainedAdmission -= minimum
		}
	}
	return &sqlSchemaDiffBudget{
		inputRemaining:    int64(sqlschemadiff.MaxDiffDefinitionBytes),
		retainedRemaining: int64(sqlschemadiff.MaxDiffDefinitionBytes),
		totalInput:        totalInput,
	}, nil
}

func (budget *sqlSchemaDiffBudget) reserveInput(byteCount int64) error {
	if budget == nil || byteCount < 0 || byteCount > budget.inputRemaining {
		return fmt.Errorf(
			"%w: aggregate CREATE TABLE input exceeds %d bytes",
			ErrSQLSchemaDiffBudget,
			sqlschemadiff.MaxDiffDefinitionBytes,
		)
	}
	budget.inputRemaining -= byteCount
	budget.inputUsed += byteCount
	return nil
}

func (budget *sqlSchemaDiffBudget) reserveRetained(byteCount int64) error {
	if budget == nil || byteCount < 0 || byteCount > budget.retainedRemaining {
		return fmt.Errorf(
			"%w: aggregate retained schema state exceeds %d bytes",
			ErrSQLSchemaDiffBudget,
			sqlschemadiff.MaxDiffDefinitionBytes,
		)
	}
	budget.retainedRemaining -= byteCount
	return nil
}

// parsedSchemaFromLease reads only preplanned exact CREATE regions through a
// source-block-verifying reader. Missing or duplicate CREATE regions remain
// zero Definitions, which the diff engine surfaces as explicit unknowns.
func (s *FileService) parsedSchemaFromLease(
	ctx context.Context,
	lease *sqlSchemaLease,
	budget *sqlSchemaDiffBudget,
	progress func(int64, string),
) ([]sqlschemadiff.Table, error) {
	if lease == nil || lease.file == nil || lease.verified == nil {
		return nil, errors.New("SQL schema parsing requires a verified analysis lease")
	}
	tables := make([]sqlschemadiff.Table, 0, len(lease.plan.reads))
	for index, read := range lease.plan.reads {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		table := sqlschemadiff.Table{Name: read.name}
		if read.hasDDL {
			readBytes := read.end - read.start
			if readBytes < 0 || readBytes > maxSQLSchemaStatementReadBytes {
				return nil, fmt.Errorf("invalid bounded schema read [%d,%d)", read.start, read.end)
			}
			if err := budget.reserveInput(readBytes); err != nil {
				return nil, err
			}
			if err := notifySQLSchemaDiffRead(ctx, lease.file.ID, read.start, read.end); err != nil {
				return nil, err
			}
			ddl, err := io.ReadAll(io.NewSectionReader(lease.verified, read.start, readBytes))
			if err != nil {
				return nil, err
			}
			if int64(len(ddl)) != readBytes {
				return nil, fmt.Errorf("schema read returned %d of %d bytes", len(ddl), readBytes)
			}
			parsed, err := sqlschemadiff.ParseTableContext(ctx, ddl)
			if err != nil {
				return nil, err
			}
			table.Definition = parsed
			if progress != nil {
				progress(budget.inputUsed, fmt.Sprintf("%d schema definitions parsed", index+1))
			}
		}
		if err := budget.reserveRetained(sqlschemadiff.TableRetainedBytes(table)); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func baseName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// SqlListPresets exposes no names while structural cleanup presets remain
// fail-closed.
func (s *FileService) SqlListPresets() []string {
	return []string{}
}

// SqlApplyPresetViaDialog is retained for generated-binding compatibility. It
// rejects before file lookup, dialogs, jobs, or filesystem work.
func (s *FileService) SqlApplyPresetViaDialog(fileID, name, a1, a2, a3, a4 string) (TransformResult, error) {
	return TransformResult{}, ErrSQLCleanupPresetsDisabled
}

func safeFileName(name string) string {
	return sqlextract.SafeFilenameComponent(name)
}

func fmtByteCount(n int64) string {
	return units.FormatBytes(n)
}
