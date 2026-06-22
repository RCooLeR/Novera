package bigfile

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/exportx"
	sqlanalyze "novera/internal/bigfile/plugins/sql/analyze"
	sqlextract "novera/internal/bigfile/plugins/sql/extract"
	sqlpreset "novera/internal/bigfile/plugins/sql/preset"
	sqlreshape "novera/internal/bigfile/plugins/sql/reshape"
	sqlschemadiff "novera/internal/bigfile/plugins/sql/schemadiff"
	"novera/internal/bigfile/replace"
	"novera/internal/bigfile/session"
)

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

// SqlAnalyze streams a single pass over the dump to discover tables and stats,
// caching the result for later extraction. This is a full pass — the UI should
// show progress for huge files.
func (s *FileService) SqlAnalyze(fileID string) (SqlSummaryResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return SqlSummaryResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	// Reuse the document's already-open descriptor (it satisfies ReaderAtSize)
	// instead of AnalyzeFile reopening the path. NOTE: this still reads the whole
	// file once; fusing the analyze pass with line indexing to avoid the second
	// full read is a separate, larger change.
	summary, err := sqlanalyze.Analyze(context.Background(), f.Doc, sqlanalyze.Options{})
	if err != nil {
		return SqlSummaryResult{}, err
	}
	s.sqlMu.Lock()
	s.sqlSummary[fileID] = summary
	s.sqlMu.Unlock()

	// Per-table byte size, from contiguous ranges (start of this table → start of
	// the next). PlanTableRanges errors when no offsets are known, so guard it.
	sizeByName := map[string]int64{}
	if ranges, perr := sqlextract.PlanTableRanges(summary, f.Doc.Size(), sqlextract.PlanOptions{}); perr == nil {
		for _, r := range ranges {
			sizeByName[r.Name] = r.Bytes
		}
	}

	tables := make([]SqlTable, 0, len(summary.Tables))
	for _, t := range summary.Tables {
		tables = append(tables, SqlTable{Name: t.Name, CreateOffset: t.CreateOffset, InsertOffset: t.InsertOffset, Bytes: sizeByName[t.Name]})
	}
	return SqlSummaryResult{
		Tables:       tables,
		CreateTables: summary.CreateTables,
		InsertTables: summary.InsertTables,
		DefinerCount: summary.DefinerCount,
		Header:       summary.MysqldumpHeader,
	}, nil
}

// SqlExtractTableViaDialog writes one table's byte range to a chosen output
// file (streamed; never materializes the whole dump).
func (s *FileService) SqlExtractTableViaDialog(fileID string, tableName string) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	s.sqlMu.Lock()
	summary, have := s.sqlSummary[fileID]
	s.sqlMu.Unlock()
	if !have {
		return TransformResult{}, errors.New("analyze the dump first")
	}
	rng, err := sqlextract.PlanExtractTable(summary, f.Doc.Size(), tableName, sqlextract.PlanOptions{})
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := saveDialog("Extract table to", safeFileName(tableName)+".sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	sum, err := exportx.ExportByteRange(context.Background(), f.Doc, f.Path, dst, rng.StartOffset, rng.EndOffset, exportx.Options{})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{
		OutputPath:     dst,
		RecordsWritten: 1,
		Note:           fmt.Sprintf("%s — %s", tableName, fmtByteCount(sum.BytesWritten)),
	}, nil
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
		out = append(out, SqlLintFinding{Severity: "warn", Title: fmt.Sprintf("%d DEFINER clauses", summary.DefinerCount), Detail: "Re-import may fail unless the definer user exists; consider the Remove DEFINER preset."})
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
	f, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	dir, err := dirDialog("Choose a folder for the per-table files")
	if err != nil || strings.TrimSpace(dir) == "" {
		return TransformResult{}, err
	}
	sum, err := sqlextract.SplitByTable(context.Background(), f.Doc, f.Path, summary,
		sqlextract.WriteOptions{PlanOptions: sqlextract.PlanOptions{OutputDir: dir}})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{
		OutputPath:     dir,
		RecordsWritten: int64(len(sum.Outputs)),
		Note:           fmt.Sprintf("%d tables · %s total", len(sum.Outputs), fmtByteCount(sum.BytesWritten)),
	}, nil
}

// SqlExtractSchemaViaDialog writes just the DDL (CREATE TABLE / structure) with
// no INSERT data. An empty tableName extracts the whole dump's schema.
func (s *FileService) SqlExtractSchemaViaDialog(fileID, tableName string) (TransformResult, error) {
	f, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	ranges, err := sqlextract.PlanTableRanges(summary, f.Doc.Size(), sqlextract.PlanOptions{})
	if err != nil {
		return TransformResult{}, err
	}

	var regions [][2]int64
	defName := "schema.sql"
	if strings.TrimSpace(tableName) == "" {
		// Whole-dump schema: leading preamble (SET NAMES …) + each table's DDL.
		if len(ranges) > 0 && ranges[0].StartOffset > 0 {
			regions = append(regions, [2]int64{0, ranges[0].StartOffset})
		}
		for _, r := range ranges {
			regions = append(regions, schemaRegion(r))
		}
	} else {
		r, ok := findRange(ranges, tableName)
		if !ok {
			return TransformResult{}, fmt.Errorf("table %q was not discovered", tableName)
		}
		regions = append(regions, schemaRegion(r))
		defName = safeFileName(tableName) + ".schema.sql"
	}

	dst, err := saveDialog("Save schema as", defName)
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	written, err := exportRanges(f.Doc, dst, regions)
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{OutputPath: dst, RecordsWritten: int64(len(regions)), Note: "schema only — " + fmtByteCount(written)}, nil
}

// SqlExtractDataViaDialog writes just the INSERT rows for a table (no DDL).
func (s *FileService) SqlExtractDataViaDialog(fileID, tableName string) (TransformResult, error) {
	f, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	ranges, err := sqlextract.PlanTableRanges(summary, f.Doc.Size(), sqlextract.PlanOptions{})
	if err != nil {
		return TransformResult{}, err
	}
	r, ok := findRange(ranges, tableName)
	if !ok {
		return TransformResult{}, fmt.Errorf("table %q was not discovered", tableName)
	}
	region := dataRegion(r)
	if region[1] <= region[0] {
		return TransformResult{}, fmt.Errorf("table %q has no INSERT data", tableName)
	}
	dst, err := saveDialog("Save data as", safeFileName(tableName)+".data.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	written, err := exportRanges(f.Doc, dst, [][2]int64{region})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{OutputPath: dst, RecordsWritten: 1, Note: "data only — " + fmtByteCount(written)}, nil
}

// sqlSummaryFor returns the file and its cached analysis, or an error to analyze first.
func (s *FileService) sqlSummaryFor(fileID string) (*session.File, sqlanalyze.Summary, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return nil, sqlanalyze.Summary{}, fmt.Errorf("unknown file id %q", fileID)
	}
	s.sqlMu.Lock()
	summary, have := s.sqlSummary[fileID]
	s.sqlMu.Unlock()
	if !have {
		return nil, sqlanalyze.Summary{}, errors.New("analyze the dump first")
	}
	return f, summary, nil
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
func schemaRegion(r sqlextract.TableRange) [2]int64 {
	start := r.StartOffset
	if r.CreateOffset >= 0 {
		start = r.CreateOffset
	}
	end := r.EndOffset
	if r.InsertOffset > start && r.InsertOffset <= r.EndOffset {
		end = r.InsertOffset
	}
	return [2]int64{start, end}
}

// dataRegion is a table's INSERT byte range (first INSERT to the next table).
func dataRegion(r sqlextract.TableRange) [2]int64 {
	start := r.StartOffset
	if r.InsertOffset >= start {
		start = r.InsertOffset
	}
	return [2]int64{start, r.EndOffset}
}

// exportRanges streams the given byte ranges of doc, in order, into one file.
func exportRanges(doc *document.FileDocument, dst string, ranges [][2]int64) (int64, error) {
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	bw := bufio.NewWriterSize(out, 1<<20)
	var total int64
	for _, rg := range ranges {
		if rg[1] <= rg[0] {
			continue
		}
		n, err := io.Copy(bw, io.NewSectionReader(doc, rg[0], rg[1]-rg[0]))
		total += n
		if err != nil {
			return total, err
		}
	}
	if err := bw.Flush(); err != nil {
		return total, err
	}
	return total, out.Sync()
}

// SqlSampleFixtureViaDialog writes a small "dev fixture" dump: each table's DDL
// plus only its first rowsPerTable INSERT rows. Turns a prod dump into a tiny,
// shareable seed without ever materialising the whole file.
func (s *FileService) SqlSampleFixtureViaDialog(fileID string, rowsPerTable int) (TransformResult, error) {
	f, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	if rowsPerTable <= 0 {
		rowsPerTable = 100
	}
	ranges, err := sqlextract.PlanTableRanges(summary, f.Doc.Size(), sqlextract.PlanOptions{})
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := saveDialog("Save dev fixture as", "fixture.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}

	out, err := os.Create(dst)
	if err != nil {
		return TransformResult{}, err
	}
	defer out.Close()
	bw := bufio.NewWriterSize(out, 1<<20)

	// Leading preamble (SET NAMES / charset) so the fixture re-imports cleanly.
	if len(ranges) > 0 && ranges[0].StartOffset > 0 {
		if err := copyRange(bw, f.Doc, 0, ranges[0].StartOffset); err != nil {
			return TransformResult{}, err
		}
	}
	for _, r := range ranges {
		sch := schemaRegion(r)
		if err := copyRange(bw, f.Doc, sch[0], sch[1]); err != nil {
			return TransformResult{}, err
		}
		dat := dataRegion(r)
		if dat[1] > dat[0] {
			sample, err := sampleInsertRows(f.Doc, dat[0], dat[1], rowsPerTable)
			if err != nil {
				return TransformResult{}, err
			}
			if _, err := bw.Write(sample); err != nil {
				return TransformResult{}, err
			}
		}
	}
	if err := bw.Flush(); err != nil {
		return TransformResult{}, err
	}
	if err := out.Sync(); err != nil {
		return TransformResult{}, err
	}
	info, _ := os.Stat(dst)
	var size int64
	if info != nil {
		size = info.Size()
	}
	return TransformResult{
		OutputPath:     dst,
		RecordsWritten: int64(len(ranges)),
		Note:           fmt.Sprintf("%d tables · ≤%d rows each · %s", len(ranges), rowsPerTable, fmtByteCount(size)),
	}, nil
}

// copyRange streams doc[start:end) into bw.
func copyRange(bw *bufio.Writer, doc *document.FileDocument, start, end int64) error {
	if end <= start {
		return nil
	}
	_, err := io.Copy(bw, io.NewSectionReader(doc, start, end-start))
	return err
}

// sampleInsertRows returns the prefix of an INSERT region doc[start:end) that
// contains the first maxRows value tuples, terminated as valid SQL. It counts
// top-level "(...)" tuples, respecting single-quoted strings with backslash and
// doubled-quote escapes, and reads the region in chunks so it stops early.
func sampleInsertRows(r io.ReaderAt, start, end int64, maxRows int) ([]byte, error) {
	var out bytes.Buffer
	rows, depth := 0, 0
	inStr, esc := false, false
	pos := start
	const chunk = 256 * 1024
	buf := make([]byte, chunk)
	for pos < end && rows < maxRows {
		want := int64(chunk)
		if want > end-pos {
			want = end - pos
		}
		n, err := r.ReadAt(buf[:want], pos)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if n == 0 {
			break
		}
		stop := -1
		for i := 0; i < n; i++ {
			c := buf[i]
			if inStr {
				if esc {
					esc = false
				} else if c == '\\' {
					esc = true
				} else if c == '\'' {
					inStr = false
				}
				continue
			}
			switch c {
			case '\'':
				inStr = true
			case '(':
				depth++
			case ')':
				if depth > 0 {
					depth--
					if depth == 0 {
						rows++
						if rows >= maxRows {
							stop = i + 1
						}
					}
				}
			}
			if stop >= 0 {
				break
			}
		}
		if stop >= 0 {
			out.Write(buf[:stop])
			break
		}
		out.Write(buf[:n])
		pos += int64(n)
	}
	trimmed := bytes.TrimRight(out.Bytes(), " \t\r\n,")
	if len(trimmed) == 0 {
		return nil, nil
	}
	tail := []byte(";\n")
	if trimmed[len(trimmed)-1] == ';' {
		tail = []byte("\n")
	}
	return append(trimmed, tail...), nil
}

// SqlReplaceViaDialog streams a find/replace (plain or regex) to a new file —
// also used for table-prefix renames. The source is never modified.
func (s *FileService) SqlReplaceViaDialog(fileID, find, replaceWith string, regex, caseInsensitive, wholeWord bool) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	if strings.TrimSpace(find) == "" {
		return TransformResult{}, errors.New("enter text to find")
	}
	dst, err := saveDialog("Save replaced copy as", "replaced.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	rules := []replace.BatchRule{{Name: "find-replace", Find: []byte(find), Replace: []byte(replaceWith)}}

	var sum replace.FileSummary
	if regex {
		sum, err = replace.ReplaceBatchRegexpFile(context.Background(), f.Path, dst, rules,
			replace.FileOptions{CaseInsensitive: caseInsensitive, WholeWord: wholeWord},
			replace.RegexOptions{CaseInsensitive: caseInsensitive})
	} else {
		sum, err = replace.ReplaceBatchPlainFile(context.Background(), f.Path, dst, rules,
			replace.FileOptions{CaseInsensitive: caseInsensitive, WholeWord: wholeWord},
			replace.BatchOptions{CaseInsensitive: caseInsensitive, WholeWord: wholeWord})
	}
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{
		OutputPath:     dst,
		RecordsWritten: sum.Matches,
		Note:           fmt.Sprintf("%d replacements", sum.Matches),
	}, nil
}

// SqlReshapeInsertsViaDialog rewrites INSERT row layout, streaming to a new file.
// mode "single" explodes extended INSERTs into one row each (good for line diffs);
// mode "multi" batches consecutive single-row INSERTs (good for fast re-import).
func (s *FileService) SqlReshapeInsertsViaDialog(fileID, mode string, batchSize int) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	m := sqlreshape.ModeSingleRow
	defName := "single-row.sql"
	if strings.EqualFold(strings.TrimSpace(mode), "multi") {
		m = sqlreshape.ModeMultiRow
		defName = "batched.sql"
	}
	dst, err := saveDialog("Save reshaped SQL as", defName)
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	sum, err := sqlreshape.ReshapeInsertsFile(context.Background(), f.Path, dst, sqlreshape.Options{
		Mode:      m,
		BatchSize: batchSize,
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
	FileA          string                       `json:"fileA"`
	FileB          string                       `json:"fileB"`
	AddedTables    []string                     `json:"addedTables"`
	RemovedTables  []string                     `json:"removedTables"`
	ChangedTables  []sqlschemadiff.TableDiff    `json:"changedTables"`
	UnchangedCount int                          `json:"unchangedCount"`
}

// SqlSchemaDiff compares the table/column structure of two analyzed dumps
// (A = baseline, B = new). Both must be analyzed first.
func (s *FileService) SqlSchemaDiff(fileIDA, fileIDB string) (SqlSchemaDiffResult, error) {
	fa, ta, err := s.parsedSchema(fileIDA)
	if err != nil {
		return SqlSchemaDiffResult{}, err
	}
	fb, tb, err := s.parsedSchema(fileIDB)
	if err != nil {
		return SqlSchemaDiffResult{}, err
	}
	res := sqlschemadiff.Diff(ta, tb)
	return SqlSchemaDiffResult{
		FileA:          baseName(fa.Path),
		FileB:          baseName(fb.Path),
		AddedTables:    res.AddedTables,
		RemovedTables:  res.RemovedTables,
		ChangedTables:  res.ChangedTables,
		UnchangedCount: res.UnchangedCount,
	}, nil
}

// parsedSchema reads each table's CREATE TABLE DDL from a dump and parses its
// columns, using the cached analysis for byte ranges.
func (s *FileService) parsedSchema(fileID string) (*session.File, []sqlschemadiff.Table, error) {
	f, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return nil, nil, err
	}
	ranges, err := sqlextract.PlanTableRanges(summary, f.Doc.Size(), sqlextract.PlanOptions{})
	if err != nil {
		return nil, nil, err
	}
	// A CREATE TABLE statement is small; bound the read so a table whose data
	// region wasn't delimited (no detected INSERT) can't force a multi-GB read
	// that ReadRange rejects and aborts the whole diff.
	const ddlMaxBytes = 8 << 20
	tables := make([]sqlschemadiff.Table, 0, len(ranges))
	for _, r := range ranges {
		// No CREATE TABLE → schema is unknown; emit an empty-column table rather
		// than parsing INSERT data rows as if they were column definitions.
		if r.CreateOffset < 0 {
			tables = append(tables, sqlschemadiff.Table{Name: r.Name})
			continue
		}
		reg := schemaRegion(r)
		if reg[1] <= reg[0] {
			tables = append(tables, sqlschemadiff.Table{Name: r.Name})
			continue
		}
		end := reg[1]
		if end-reg[0] > ddlMaxBytes {
			end = reg[0] + ddlMaxBytes
		}
		ddl, rerr := f.Doc.ReadRange(reg[0], end)
		if rerr != nil {
			return nil, nil, rerr
		}
		tables = append(tables, sqlschemadiff.Table{Name: r.Name, Columns: sqlschemadiff.ParseColumns(ddl)})
	}
	return f, tables, nil
}

func baseName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// SqlListPresets returns the available SQL cleanup preset names.
func (s *FileService) SqlListPresets() []string {
	return append([]string(nil), sqlpreset.PresetNames...)
}

// SqlApplyPresetViaDialog runs a named cleanup preset, streaming the result to a
// chosen file. Presets that need arguments (e.g. change-database) take them via
// a1..a4; the source is never modified.
func (s *FileService) SqlApplyPresetViaDialog(fileID, name, a1, a2, a3, a4 string) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	cfg, err := sqlpreset.Build(name, a1, a2, a3, a4)
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := saveDialog("Save cleaned SQL as", "cleaned.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	ci := !cfg.CaseSensitive
	var sum replace.FileSummary
	switch cfg.Mode {
	case sqlpreset.ModeRegex:
		rules := []replace.BatchRule{{Name: name, Find: []byte(cfg.Search), Replace: []byte(cfg.Replace)}}
		sum, err = replace.ReplaceBatchRegexpFile(context.Background(), f.Path, dst, rules,
			replace.FileOptions{CaseInsensitive: ci, WholeWord: cfg.WholeWord},
			replace.RegexOptions{CaseInsensitive: ci})
	case sqlpreset.ModeRegexBatch:
		sum, err = replace.ReplaceBatchRegexpFile(context.Background(), f.Path, dst, cfg.BatchRules,
			replace.FileOptions{CaseInsensitive: ci, WholeWord: cfg.WholeWord},
			replace.RegexOptions{CaseInsensitive: ci})
	case sqlpreset.ModeBatch:
		sum, err = replace.ReplaceBatchPlainFile(context.Background(), f.Path, dst, cfg.BatchRules,
			replace.FileOptions{CaseInsensitive: ci, WholeWord: cfg.WholeWord},
			replace.BatchOptions{CaseInsensitive: ci, WholeWord: cfg.WholeWord})
	default: // plain
		rules := []replace.BatchRule{{Name: name, Find: []byte(cfg.Search), Replace: []byte(cfg.Replace)}}
		sum, err = replace.ReplaceBatchPlainFile(context.Background(), f.Path, dst, rules,
			replace.FileOptions{CaseInsensitive: ci, WholeWord: cfg.WholeWord},
			replace.BatchOptions{CaseInsensitive: ci, WholeWord: cfg.WholeWord})
	}
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{OutputPath: dst, RecordsWritten: sum.Matches, Note: cfg.Summary}, nil
}

func safeFileName(name string) string {
	name = strings.TrimSpace(name)
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", "`", "")
	name = repl.Replace(name)
	if name == "" {
		return "table"
	}
	return name
}

func fmtByteCount(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
