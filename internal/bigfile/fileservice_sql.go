package bigfile

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

type cachedSQLSummary struct {
	Generation uint64
	Summary    sqlanalyze.Summary
}

func (s *FileService) invalidateSQLSummary(fileID string) {
	s.sqlMu.Lock()
	delete(s.sqlSummary, fileID)
	s.sqlMu.Unlock()
}

// SqlAnalyze streams a single pass over the dump to discover tables and stats,
// caching the result for later extraction. This is a full pass — the UI should
// show progress for huge files.
func (s *FileService) SqlAnalyze(fileID string) (SqlSummaryResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return SqlSummaryResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	// Reuse the document's already-open descriptor (it satisfies ReaderAtSize)
	// instead of AnalyzeFile reopening the path. NOTE: this still reads the whole
	// file once; fusing the analyze pass with line indexing to avoid the second
	// full read is a separate, larger change.
	summary, err := withJobResult(s, "Analyze SQL dump", func(ctx context.Context, progress func(int64, string)) (sqlanalyze.Summary, error) {
		return sqlanalyze.Analyze(ctx, f.Doc, sqlanalyze.Options{
			Progress: func(p sqlanalyze.Progress) { progress(p.BytesProcessed, "bytes analyzed") },
		})
	})
	if err != nil {
		return SqlSummaryResult{}, err
	}
	s.sqlMu.Lock()
	s.sqlSummary[fileID] = cachedSQLSummary{Generation: f.Generation, Summary: summary}
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
	f, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	defer f.Release()
	rng, err := sqlextract.PlanExtractTable(summary, f.Doc.Size(), tableName, sqlextract.PlanOptions{})
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := saveDialog("Extract table to", safeFileName(tableName)+".sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	sum, err := withJobResult(s, "Extract SQL table", func(ctx context.Context, progress func(int64, string)) (exportx.Summary, error) {
		return exportx.ExportByteRange(ctx, f.Doc, f.Path, dst, rng.StartOffset, rng.EndOffset, exportx.Options{
			Progress: func(done int64, _ int64) { progress(done, "bytes written") },
		})
	})
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
	defer f.Release()
	dir, err := dirDialog("Choose a folder for the per-table files")
	if err != nil || strings.TrimSpace(dir) == "" {
		return TransformResult{}, err
	}
	sum, err := withJobResult(s, "Split SQL dump", func(ctx context.Context, progress func(int64, string)) (sqlextract.WriteSummary, error) {
		return sqlextract.SplitByTable(ctx, f.Doc, f.Path, summary, sqlextract.WriteOptions{
			PlanOptions: sqlextract.PlanOptions{OutputDir: dir},
			Progress:    func(done int64, _ int64, _ int) { progress(done, "bytes written") },
		})
	})
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
	defer f.Release()
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
	written, err := withJobResult(s, "Extract SQL schema", func(ctx context.Context, progress func(int64, string)) (int64, error) {
		return exportRanges(ctx, f.Doc, f.Path, dst, regions, progress)
	})
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
	defer f.Release()
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
	written, err := withJobResult(s, "Extract SQL data", func(ctx context.Context, progress func(int64, string)) (int64, error) {
		return exportRanges(ctx, f.Doc, f.Path, dst, [][2]int64{region}, progress)
	})
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
	cached, have := s.sqlSummary[fileID]
	s.sqlMu.Unlock()
	if !have || cached.Generation != f.Generation {
		f.Release()
		return nil, sqlanalyze.Summary{}, errors.New("analyze the dump first")
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
func exportRanges(ctx context.Context, doc *document.FileDocument, sourcePath, dst string, ranges [][2]int64, progress func(int64, string)) (int64, error) {
	return writeSafeOutput(doc, sourcePath, dst, func(out io.Writer) error {
		bw := bufio.NewWriterSize(out, 1<<20)
		var written int64
		for _, rg := range ranges {
			if err := ctx.Err(); err != nil {
				return err
			}
			if rg[1] <= rg[0] {
				continue
			}
			n, err := io.Copy(bw, &jobContextReader{ctx: ctx, reader: io.NewSectionReader(doc, rg[0], rg[1]-rg[0])})
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
	})
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
	f, summary, err := s.sqlSummaryFor(fileID)
	if err != nil {
		return TransformResult{}, err
	}
	defer f.Release()
	rowsPerTable = clampRequestInt(rowsPerTable, defaultSQLFixtureRows, maxSQLFixtureRows)
	ranges, err := sqlextract.PlanTableRanges(summary, f.Doc.Size(), sqlextract.PlanOptions{})
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := saveDialog("Save dev fixture as", "fixture.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}

	size, err := withJobResult(s, "Create SQL fixture", func(ctx context.Context, progress func(int64, string)) (int64, error) {
		return writeSafeOutput(f.Doc, f.Path, dst, func(out io.Writer) error {
			bw := bufio.NewWriterSize(out, 1<<20)
			var processed int64

			// Leading preamble (SET NAMES / charset) so the fixture re-imports cleanly.
			if len(ranges) > 0 && ranges[0].StartOffset > 0 {
				n, err := copyRange(ctx, bw, f.Doc, 0, ranges[0].StartOffset)
				processed += n
				progress(processed, "bytes read")
				if err != nil {
					return err
				}
			}
			for _, r := range ranges {
				sch := schemaRegion(r)
				n, err := copyRange(ctx, bw, f.Doc, sch[0], sch[1])
				processed += n
				progress(processed, "bytes read")
				if err != nil {
					return err
				}
				dat := dataRegion(r)
				if dat[1] > dat[0] {
					sample, read, err := sampleInsertRows(ctx, f.Doc, dat[0], dat[1], rowsPerTable)
					processed += read
					progress(processed, "bytes read")
					if err != nil {
						return err
					}
					if _, err := bw.Write(sample); err != nil {
						return err
					}
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return bw.Flush()
		})
	})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{
		OutputPath:     dst,
		RecordsWritten: int64(len(ranges)),
		Note:           fmt.Sprintf("%d tables · ≤%d rows each · %s", len(ranges), rowsPerTable, fmtByteCount(size)),
	}, nil
}

// copyRange streams doc[start:end) into bw.
func copyRange(ctx context.Context, bw *bufio.Writer, doc *document.FileDocument, start, end int64) (int64, error) {
	if end <= start {
		return 0, nil
	}
	return io.Copy(bw, &jobContextReader{ctx: ctx, reader: io.NewSectionReader(doc, start, end-start)})
}

// sampleInsertRows returns the prefix of an INSERT region doc[start:end) that
// contains the first maxRows value tuples, terminated as valid SQL. It counts
// top-level "(...)" tuples, respecting single-quoted strings with backslash and
// doubled-quote escapes, and reads the region in chunks so it stops early.
func sampleInsertRows(ctx context.Context, r io.ReaderAt, start, end int64, maxRows int) ([]byte, int64, error) {
	var out bytes.Buffer
	rows, depth := 0, 0
	inStr, esc := false, false
	pos := start
	var bytesRead int64
	const chunk = 256 * 1024
	buf := make([]byte, chunk)
	for pos < end && rows < maxRows {
		if err := ctx.Err(); err != nil {
			return nil, bytesRead, err
		}
		want := int64(chunk)
		if want > end-pos {
			want = end - pos
		}
		n, err := r.ReadAt(buf[:want], pos)
		bytesRead += int64(n)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, bytesRead, err
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
		return nil, bytesRead, nil
	}
	tail := []byte(";\n")
	if trimmed[len(trimmed)-1] == ';' {
		tail = []byte("\n")
	}
	return append(trimmed, tail...), bytesRead, nil
}

// SqlReplaceViaDialog streams a find/replace (plain or regex) to a new file —
// also used for table-prefix renames. The source is never modified.
func (s *FileService) SqlReplaceViaDialog(fileID, find, replaceWith string, regex, caseInsensitive, wholeWord bool) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if strings.TrimSpace(find) == "" {
		return TransformResult{}, errors.New("enter text to find")
	}
	dst, err := saveDialog("Save replaced copy as", "replaced.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	rules := []replace.BatchRule{{Name: "find-replace", Find: []byte(find), Replace: []byte(replaceWith)}}

	sum, err := withJobResult(s, "Replace SQL text", func(ctx context.Context, progress func(int64, string)) (replace.FileSummary, error) {
		fileOpts := replace.FileOptions{
			CaseInsensitive: caseInsensitive,
			WholeWord:       wholeWord,
			Progress:        func(p replace.Progress) { progress(p.BytesProcessed, "bytes processed") },
		}
		if regex {
			return replace.ReplaceBatchRegexpFile(ctx, f.Path, dst, rules, fileOpts,
				replace.RegexOptions{CaseInsensitive: caseInsensitive})
		}
		return replace.ReplaceBatchPlainFile(ctx, f.Path, dst, rules, fileOpts,
			replace.BatchOptions{CaseInsensitive: caseInsensitive, WholeWord: wholeWord})
	})
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
	defer f.Release()
	batchSize = clampRequestInt(batchSize, defaultSQLBatchRows, maxSQLBatchRows)
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
	sum, err := withJobResult(s, "Reshape SQL inserts", func(ctx context.Context, _ func(int64, string)) (sqlreshape.Summary, error) {
		return sqlreshape.ReshapeInsertsFile(ctx, f.Path, dst, sqlreshape.Options{
			Mode:      m,
			BatchSize: batchSize,
		})
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

// SqlSchemaDiff compares the table/column structure of two analyzed dumps
// (A = baseline, B = new). Both must be analyzed first.
func (s *FileService) SqlSchemaDiff(fileIDA, fileIDB string) (SqlSchemaDiffResult, error) {
	fa, ta, err := s.parsedSchema(fileIDA)
	if err != nil {
		return SqlSchemaDiffResult{}, err
	}
	defer fa.Release()
	fb, tb, err := s.parsedSchema(fileIDB)
	if err != nil {
		return SqlSchemaDiffResult{}, err
	}
	defer fb.Release()
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
		f.Release()
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
			f.Release()
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
	defer f.Release()
	cfg, err := sqlpreset.Build(name, a1, a2, a3, a4)
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := saveDialog("Save cleaned SQL as", "cleaned.sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	ci := !cfg.CaseSensitive
	sum, err := withJobResult(s, "Apply SQL preset", func(ctx context.Context, progress func(int64, string)) (replace.FileSummary, error) {
		fileOpts := replace.FileOptions{
			CaseInsensitive: ci,
			WholeWord:       cfg.WholeWord,
			Progress:        func(p replace.Progress) { progress(p.BytesProcessed, "bytes processed") },
		}
		switch cfg.Mode {
		case sqlpreset.ModeRegex:
			rules := []replace.BatchRule{{Name: name, Find: []byte(cfg.Search), Replace: []byte(cfg.Replace)}}
			return replace.ReplaceBatchRegexpFile(ctx, f.Path, dst, rules, fileOpts,
				replace.RegexOptions{CaseInsensitive: ci})
		case sqlpreset.ModeRegexBatch:
			return replace.ReplaceBatchRegexpFile(ctx, f.Path, dst, cfg.BatchRules, fileOpts,
				replace.RegexOptions{CaseInsensitive: ci})
		case sqlpreset.ModeBatch:
			return replace.ReplaceBatchPlainFile(ctx, f.Path, dst, cfg.BatchRules, fileOpts,
				replace.BatchOptions{CaseInsensitive: ci, WholeWord: cfg.WholeWord})
		default:
			rules := []replace.BatchRule{{Name: name, Find: []byte(cfg.Search), Replace: []byte(cfg.Replace)}}
			return replace.ReplaceBatchPlainFile(ctx, f.Path, dst, rules, fileOpts,
				replace.BatchOptions{CaseInsensitive: ci, WholeWord: cfg.WholeWord})
		}
	})
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
