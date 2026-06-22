package bigfile

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/bigfile/plugins/csv"
)

const csvSampleBytes int64 = 2 << 20 // bounded sample for inspect/schema/preview

// delimiterRune parses a delimiter string ("," ";" "|" or a tab) to a rune.
func delimiterRune(s string) rune {
	switch s {
	case "", "comma":
		return ','
	case "\t", "\\t", "tab":
		return '\t'
	case "semicolon":
		return ';'
	case "pipe":
		return '|'
	case "space":
		return ' '
	}
	r := []rune(s)
	if len(r) == 0 {
		return ','
	}
	return r[0]
}

func delimiterString(r rune) string {
	if r == 0 {
		return ","
	}
	return string(r)
}

func (s *FileService) csvSampleReader(fileID string) (io.Reader, string, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return nil, "", fmt.Errorf("unknown file id %q", fileID)
	}
	n := f.Doc.Size()
	if n > csvSampleBytes {
		n = csvSampleBytes
	}
	return io.NewSectionReader(f.Doc, 0, n), f.Path, nil
}

// ---- inspect (delimiter detection) -------------------------------------

type CsvDelimiterOption struct {
	Delimiter string  `json:"delimiter"`
	Name      string  `json:"name"`
	Columns   int     `json:"columns"`
	Score     float64 `json:"score"`
}

type CsvInspectResult struct {
	Delimiter     string               `json:"delimiter"`
	DelimiterName string               `json:"delimiterName"`
	Confidence    string               `json:"confidence"`
	Columns       int                  `json:"columns"`
	HasHeader     bool                 `json:"hasHeader"`
	Candidates    []CsvDelimiterOption `json:"candidates"`
	Warnings      []string             `json:"warnings"`
}

// CsvInspect detects the most likely delimiter (and ranked alternatives) from a
// bounded sample, so the UI can preselect it and offer overrides.
func (s *FileService) CsvInspect(fileID string) (CsvInspectResult, error) {
	r, _, err := s.csvSampleReader(fileID)
	if err != nil {
		return CsvInspectResult{}, err
	}
	rep, err := csv.InspectReaderContext(context.Background(), r, csv.InspectOptions{MaxBytes: csvSampleBytes, MaxRows: 1000})
	if err != nil {
		return CsvInspectResult{}, err
	}
	cands := make([]CsvDelimiterOption, 0, len(rep.Candidates))
	for _, c := range rep.Candidates {
		cands = append(cands, CsvDelimiterOption{
			Delimiter: delimiterString(c.Delimiter),
			Name:      c.Name,
			Columns:   c.Columns,
			Score:     c.Score,
		})
	}
	return CsvInspectResult{
		Delimiter:     delimiterString(rep.Delimiter),
		DelimiterName: rep.DelimiterName,
		Confidence:    rep.Confidence,
		Columns:       rep.Columns,
		HasHeader:     rep.HasHeader,
		Candidates:    cands,
		Warnings:      rep.Warnings,
	}, nil
}

// ---- schema / preview ---------------------------------------------------

type CsvColumn struct {
	Name    string   `json:"name"`
	SQLType string   `json:"sqlType"`
	NonNull int      `json:"nonNull"`
	Null    int      `json:"null"`
	Samples []string `json:"samples"`
}

type CsvSchemaResult struct {
	Columns  []CsvColumn `json:"columns"`
	HasHeader bool       `json:"hasHeader"`
	Warnings []string    `json:"warnings"`
}

func (s *FileService) CsvSchema(fileID string, delimiter string, hasHeader bool) (CsvSchemaResult, error) {
	r, _, err := s.csvSampleReader(fileID)
	if err != nil {
		return CsvSchemaResult{}, err
	}
	rep, err := csv.InferSchemaContext(context.Background(), r, csv.SchemaOptions{
		Delimiter: delimiterRune(delimiter),
		HasHeader: hasHeader,
		MaxBytes:  csvSampleBytes,
		MaxRows:   2000,
	})
	if err != nil {
		return CsvSchemaResult{}, err
	}
	cols := make([]CsvColumn, 0, len(rep.Columns))
	for _, c := range rep.Columns {
		cols = append(cols, CsvColumn{Name: c.Name, SQLType: c.SQLType, NonNull: c.NonNullCount, Null: c.NullCount, Samples: c.SampleValues})
	}
	return CsvSchemaResult{Columns: cols, HasHeader: rep.HasHeader, Warnings: rep.Warnings}, nil
}

type CsvPreviewResult struct {
	Header   []string   `json:"header"`
	Rows     [][]string `json:"rows"`
	Warnings []string   `json:"warnings"`
}

func (s *FileService) CsvPreview(fileID string, delimiter string, hasHeader bool, maxRows int) (CsvPreviewResult, error) {
	r, _, err := s.csvSampleReader(fileID)
	if err != nil {
		return CsvPreviewResult{}, err
	}
	if maxRows <= 0 {
		maxRows = 50
	}
	rep, err := csv.PreviewRowsContext(context.Background(), r, csv.PreviewOptions{
		Delimiter: delimiterRune(delimiter),
		HasHeader: hasHeader,
		MaxBytes:  csvSampleBytes,
		MaxRows:   maxRows,
	})
	if err != nil {
		return CsvPreviewResult{}, err
	}
	return CsvPreviewResult{Header: rep.Header, Rows: rep.Rows, Warnings: rep.Warnings}, nil
}

// ---- windowed grid (spreadsheet view) -----------------------------------

type CsvGridResult struct {
	StartByte int64      `json:"startByte"`
	NextByte  int64      `json:"nextByte"`
	StartRow  int64      `json:"startRow"` // approx global row of the first row
	Rows      [][]string `json:"rows"`
	Columns   int        `json:"columns"`
	AtBof     bool       `json:"atBof"`
	AtEof     bool       `json:"atEof"`
}

// GetCsvGrid parses a bounded, line-aligned byte window into CSV rows for the
// grid view. Rows are returned as data (no header special-casing); the frontend
// supplies column labels from the schema. Scrolling loads adjacent windows.
func (s *FileService) GetCsvGrid(fileID string, delimiter string, startByte int64, maxBytes int) (CsvGridResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return CsvGridResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	if maxBytes <= 0 {
		maxBytes = 128 * 1024
	}
	size := f.Doc.Size()
	readRange := func(a, b int64) ([]byte, error) { return f.Doc.ReadRange(a, b) }
	if startByte < 0 {
		startByte = 0
	}
	if startByte > size {
		startByte = size
	}
	aligned, err := alignToLineStart(readRange, startByte, int64(maxBytes))
	if err != nil {
		return CsvGridResult{}, err
	}
	end := aligned + int64(maxBytes)
	if end > size {
		end = size
	}
	raw, err := readRange(aligned, end)
	if err != nil {
		return CsvGridResult{}, err
	}
	if end < size {
		if nl := lastIndexByte(raw, '\n'); nl >= 0 {
			raw = raw[:nl+1]
			end = aligned + int64(nl+1)
		}
	}
	rep, err := csv.PreviewRowsContext(context.Background(), bytes.NewReader(raw), csv.PreviewOptions{
		Delimiter: delimiterRune(delimiter),
		HasHeader: false,
		MaxBytes:  int64(len(raw)),
		MaxRows:   200000,
	})
	if err != nil {
		return CsvGridResult{}, err
	}
	cols := 0
	for _, r := range rep.Rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	startRow, _ := f.Doc.ApproxOffsetToLine(aligned)
	return CsvGridResult{
		StartByte: aligned,
		NextByte:  end,
		StartRow:  startRow,
		Rows:      rep.Rows,
		Columns:   cols,
		AtBof:     aligned == 0,
		AtEof:     end >= size,
	}, nil
}

// ---- transforms (stream to a chosen output file) ------------------------

type TransformResult struct {
	OutputPath     string `json:"outputPath"`
	RecordsRead    int64  `json:"recordsRead"`
	RecordsWritten int64  `json:"recordsWritten"`
	Note           string `json:"note"`
}

func saveDialog(message, defaultName string) (string, error) {
	d := application.Get().Dialog.SaveFile().SetMessage(message)
	if defaultName != "" {
		d = d.SetFilename(defaultName)
	}
	return d.PromptForSingleSelection()
}

// dirDialog prompts for an existing/new folder and returns its path ("" if cancelled).
func dirDialog(message string) (string, error) {
	d := application.Get().Dialog.OpenFile().
		SetMessage(message).
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true)
	return d.PromptForSingleSelection()
}

// CsvProfileResult is a per-column data profile over a bounded sample.
type CsvProfileResult struct {
	Columns        []csv.ColumnProfile `json:"columns"`
	RecordsScanned int                 `json:"recordsScanned"`
	RaggedRows     int                 `json:"raggedRows"`
	Truncated      bool                `json:"truncated"`
}

// CsvProfile profiles each column (null %, distinct, min/max, top values) and
// counts ragged rows over a bounded sample.
func (s *FileService) CsvProfile(fileID, delimiter string, hasHeader bool) (CsvProfileResult, error) {
	r, _, err := s.csvSampleReader(fileID)
	if err != nil {
		return CsvProfileResult{}, err
	}
	rep, err := csv.ProfileColumns(context.Background(), r, csv.SchemaOptions{
		Delimiter:  delimiterRune(delimiter),
		HasHeader:  hasHeader,
		MaxBytes:   csvSampleBytes,
		NullValues: []string{"", "NULL", "null", "\\N"},
	})
	if err != nil {
		return CsvProfileResult{}, err
	}
	return CsvProfileResult{
		Columns:        rep.Columns,
		RecordsScanned: rep.RecordsScanned,
		RaggedRows:     rep.RaggedRows,
		Truncated:      rep.TruncatedSample,
	}, nil
}

// CsvProjectViaDialog writes a new CSV keeping only keepIndices (0-based) in the
// given order — used for drop-column and reorder.
func (s *FileService) CsvProjectViaDialog(fileID string, delimiter string, keepIndices []int) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	dst, err := saveDialog("Save projected CSV as", "projected.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	sum, err := csv.ProjectColumnsFile(context.Background(), f.Path, dst, csv.ProjectOptions{
		Delimiter: delimiterRune(delimiter),
		Columns:   keepIndices,
	})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RecordsWritten, Note: fmt.Sprintf("%d columns kept", sum.ColumnsWritten)}, nil
}

// CsvAddColumnViaDialog writes a new CSV with a constant column appended to every
// row (including the header, which becomes the value).
func (s *FileService) CsvAddColumnViaDialog(fileID string, delimiter string, columnCount int, value string) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	dst, err := saveDialog("Save CSV with added column as", "with-column.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	keep := make([]int, 0, columnCount+1)
	for i := 0; i < columnCount; i++ {
		keep = append(keep, i)
	}
	keep = append(keep, columnCount) // out-of-range index -> filled with MissingValue
	sum, err := csv.ProjectColumnsFile(context.Background(), f.Path, dst, csv.ProjectOptions{
		Delimiter:         delimiterRune(delimiter),
		Columns:           keep,
		AllowShortRecords: true,
		MissingValue:      value,
	})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RecordsWritten, Note: "constant column added"}, nil
}

// CsvToSQLPreview returns a short sample of the generated SQL.
func (s *FileService) CsvToSQLPreview(fileID, delimiter, tableName string, hasHeader, includeCreate bool) (string, error) {
	r, _, err := s.csvSampleReader(fileID)
	if err != nil {
		return "", err
	}
	rep, err := csv.PreviewSQLConversionContext(context.Background(), r, csv.SQLPreviewOptions{
		SQLConvertOptions: csv.SQLConvertOptions{
			Delimiter:          delimiterRune(delimiter),
			TableName:          sqlTableName(tableName),
			HasHeader:          hasHeader,
			IncludeCreateTable: includeCreate,
		},
		MaxBytes: csvSampleBytes,
		MaxRows:  20,
	})
	if err != nil {
		return "", err
	}
	return rep.SQL, nil
}

// CsvToSQLViaDialog streams the whole CSV to a .sql file of INSERTs.
func (s *FileService) CsvToSQLViaDialog(fileID, delimiter, tableName string, hasHeader, includeCreate bool) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	dst, err := saveDialog("Save SQL as", sqlTableName(tableName)+".sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	sum, err := csv.ConvertToSQLFile(context.Background(), f.Path, dst, csv.SQLConvertOptions{
		Delimiter:          delimiterRune(delimiter),
		TableName:          sqlTableName(tableName),
		HasHeader:          hasHeader,
		IncludeCreateTable: includeCreate,
	})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RowsWritten, Note: fmt.Sprintf("table %q", sum.TableName)}, nil
}

func sqlTableName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "imported"
	}
	return name
}

// CsvSqlColumnConfig describes one output column of a CSV→SQL conversion.
type CsvSqlColumnConfig struct {
	Source  int    `json:"source"`  // 0-based source field index
	Name    string `json:"name"`    // output column name
	Type    string `json:"type"`    // SQL type (used only when includeCreate)
	Include bool   `json:"include"` // whether to emit this column
}

// CsvSqlConfig is the full, flexible CSV→SQL conversion request.
type CsvSqlConfig struct {
	Delimiter     string               `json:"delimiter"`
	HasHeader     bool                 `json:"hasHeader"`
	TableName     string               `json:"tableName"`
	Columns       []CsvSqlColumnConfig `json:"columns"`
	IncludeCreate bool                 `json:"includeCreate"`
	InsertMode    string               `json:"insertMode"` // insert | ignore | replace
	BatchSize     int                  `json:"batchSize"`
	NullValues    []string             `json:"nullValues"`
	OnInvalid     string               `json:"onInvalid"` // fail | skip-row | replace
}

func csvSqlOptions(cfg CsvSqlConfig) (csv.SQLConvertOptions, error) {
	var src []int
	var names, types []string
	for _, c := range cfg.Columns {
		if !c.Include {
			continue
		}
		src = append(src, c.Source)
		names = append(names, strings.TrimSpace(c.Name))
		types = append(types, c.Type)
	}
	if len(names) == 0 {
		return csv.SQLConvertOptions{}, fmt.Errorf("select at least one column to convert")
	}
	verb := "INSERT INTO"
	switch strings.ToLower(strings.TrimSpace(cfg.InsertMode)) {
	case "ignore":
		verb = "INSERT IGNORE INTO"
	case "replace":
		verb = "REPLACE INTO"
	}
	return csv.SQLConvertOptions{
		Delimiter:          delimiterRune(cfg.Delimiter),
		TableName:          sqlTableName(cfg.TableName),
		HasHeader:          cfg.HasHeader,
		Columns:            names,
		ColumnTypes:        types,
		SourceColumns:      src,
		IncludeCreateTable: cfg.IncludeCreate,
		InsertVerb:         verb,
		InsertBatchSize:    cfg.BatchSize,
		NullValues:         cfg.NullValues,
		OnInvalidValue:     csv.InvalidValuePolicy(strings.TrimSpace(cfg.OnInvalid)),
	}, nil
}

// CsvRedactColumn is one column to mask in a redaction.
type CsvRedactColumn struct {
	Index int    `json:"index"`
	Mode  string `json:"mode"` // null | fixed | hash | email
}

// CsvRedactViaDialog writes an anonymized copy of the CSV with the chosen columns
// masked, for sharing a dataset without leaking PII. Source is never modified.
func (s *FileService) CsvRedactViaDialog(fileID, delimiter string, hasHeader bool, columns []CsvRedactColumn, replacement string) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	cols := map[int]csv.RedactMode{}
	for _, c := range columns {
		cols[c.Index] = csv.RedactMode(strings.TrimSpace(c.Mode))
	}
	dst, err := saveDialog("Save redacted copy as", "redacted.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withJob("Redact CSV", func(ctx context.Context, progress func(int64, string)) (TransformResult, error) {
		sum, err := csv.RedactColumnsFile(ctx, f.Path, dst, csv.RedactOptions{
			Delimiter:   delimiterRune(delimiter),
			HasHeader:   hasHeader,
			Columns:     cols,
			Replacement: replacement,
			Progress:    func(r int64, _ int64) { progress(r, "rows read") },
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{
			OutputPath:     dst,
			RecordsRead:    sum.RecordsRead,
			RecordsWritten: sum.RecordsWritten,
			Note:           fmt.Sprintf("%d cells masked across %d columns", sum.CellsMasked, len(cols)),
		}, nil
	})
}

// CsvFilterViaDialog writes a new CSV keeping only rows where the chosen column
// matches op/value (eq, ne, contains, gt, lt, empty, nonempty). Source untouched.
func (s *FileService) CsvFilterViaDialog(fileID, delimiter string, hasHeader bool, column int, op, value string, negate bool) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	dst, err := saveDialog("Save filtered CSV as", "filtered.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withJob("Filter CSV", func(ctx context.Context, progress func(int64, string)) (TransformResult, error) {
		sum, err := csv.FilterRowsFile(ctx, f.Path, dst, csv.FilterOptions{
			Delimiter: delimiterRune(delimiter),
			HasHeader: hasHeader,
			Column:    column,
			Op:        strings.TrimSpace(op),
			Value:     value,
			Negate:    negate,
			Progress:  func(r int64) { progress(r, "rows read") },
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{
			OutputPath:     dst,
			RecordsRead:    sum.RecordsRead,
			RecordsWritten: sum.RecordsWritten,
			Note:           "rows matching filter kept",
		}, nil
	})
}

// CsvDedupeViaDialog writes a new CSV dropping duplicate rows — by a key column
// (keyColumn>=0) or the whole row (keyColumn<0). First occurrence wins.
func (s *FileService) CsvDedupeViaDialog(fileID, delimiter string, hasHeader bool, keyColumn int) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	dst, err := saveDialog("Save deduplicated CSV as", "deduped.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withJob("Deduplicate CSV", func(ctx context.Context, progress func(int64, string)) (TransformResult, error) {
		sum, err := csv.DedupeRowsFile(ctx, f.Path, dst, csv.DedupeOptions{
			Delimiter: delimiterRune(delimiter),
			HasHeader: hasHeader,
			KeyColumn: keyColumn,
			Progress:  func(r int64) { progress(r, "rows read") },
		})
		if err != nil {
			return TransformResult{}, err
		}
		dropped := sum.RecordsRead - sum.RecordsWritten
		return TransformResult{
			OutputPath:     dst,
			RecordsRead:    sum.RecordsRead,
			RecordsWritten: sum.RecordsWritten,
			Note:           fmt.Sprintf("%d duplicate rows removed", dropped),
		}, nil
	})
}

// CsvSampleViaDialog writes a new CSV keeping every Nth data row (header kept),
// for shrinking a huge dump to a representative slice.
func (s *FileService) CsvSampleViaDialog(fileID, delimiter string, hasHeader bool, everyN int) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	if everyN <= 0 {
		everyN = 10
	}
	dst, err := saveDialog("Save sampled CSV as", "sampled.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withJob("Sample CSV", func(ctx context.Context, progress func(int64, string)) (TransformResult, error) {
		sum, err := csv.SampleRowsFile(ctx, f.Path, dst, csv.SampleOptions{
			Delimiter: delimiterRune(delimiter),
			HasHeader: hasHeader,
			EveryN:    everyN,
			Progress:  func(r int64) { progress(r, "rows read") },
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{
			OutputPath:     dst,
			RecordsRead:    sum.RecordsRead,
			RecordsWritten: sum.RecordsWritten,
			Note:           fmt.Sprintf("kept every %dth row", everyN),
		}, nil
	})
}

// CsvExportJSONLViaDialog streams the CSV to newline-delimited JSON.
func (s *FileService) CsvExportJSONLViaDialog(fileID, delimiter string, hasHeader, numberKeys bool) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	dst, err := saveDialog("Export JSON Lines as", "export.jsonl")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withJob("Export JSONL", func(ctx context.Context, progress func(int64, string)) (TransformResult, error) {
		sum, err := csv.ExportJSONLFile(ctx, f.Path, dst, csv.JSONLOptions{
			Delimiter:  delimiterRune(delimiter),
			HasHeader:  hasHeader,
			NumberKeys: numberKeys,
			Progress:   func(r int64) { progress(r, "rows read") },
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RecordsWritten, Note: "JSON Lines"}, nil
	})
}

// CsvExportSQLiteViaDialog streams the CSV into a new SQLite .db file.
func (s *FileService) CsvExportSQLiteViaDialog(fileID, delimiter string, hasHeader bool, tableName string, typedCells bool) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	dst, err := saveDialog("Export SQLite database as", "export.db")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withJob("Export SQLite", func(ctx context.Context, progress func(int64, string)) (TransformResult, error) {
		sum, err := csv.ExportSQLiteFile(ctx, f.Path, dst, csv.SQLiteOptions{
			Delimiter:  delimiterRune(delimiter),
			HasHeader:  hasHeader,
			TableName:  sqlTableName(tableName),
			TypedCells: typedCells,
			Progress:   func(r int64) { progress(r, "rows inserted") },
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RecordsWritten, Note: fmt.Sprintf("table %q", sqlTableName(tableName))}, nil
	})
}

// CsvExportXLSXViaDialog streams the CSV into a new .xlsx workbook.
func (s *FileService) CsvExportXLSXViaDialog(fileID, delimiter string, hasHeader bool, sheetName string, typedCells bool) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	dst, err := saveDialog("Export Excel workbook as", "export.xlsx")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withJob("Export Excel", func(ctx context.Context, progress func(int64, string)) (TransformResult, error) {
		sum, err := csv.ExportXLSXFile(ctx, f.Path, dst, csv.XLSXOptions{
			Delimiter:  delimiterRune(delimiter),
			HasHeader:  hasHeader,
			SheetName:  strings.TrimSpace(sheetName),
			TypedCells: typedCells,
			Progress:   func(r int64) { progress(r, "rows written") },
		})
		if err != nil {
			return TransformResult{}, err
		}
		note := "Excel workbook"
		if sum.Truncated {
			note = fmt.Sprintf("Excel workbook (truncated at %d rows — sheet limit)", sum.RecordsWritten)
		}
		return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RecordsWritten, Note: note}, nil
	})
}

// CsvMarkdownPreview returns the current preview as a Markdown table (for copy).
func (s *FileService) CsvMarkdownPreview(fileID, delimiter string, hasHeader bool, maxRows int) (string, error) {
	r, _, err := s.csvSampleReader(fileID)
	if err != nil {
		return "", err
	}
	if maxRows <= 0 {
		maxRows = 50
	}
	rep, err := csv.PreviewRowsContext(context.Background(), r, csv.PreviewOptions{
		Delimiter: delimiterRune(delimiter),
		HasHeader: hasHeader,
		MaxBytes:  csvSampleBytes,
		MaxRows:   maxRows,
	})
	if err != nil {
		return "", err
	}
	return csv.MarkdownPreview(rep.Header, rep.Rows), nil
}

// CsvToSQLConfigPreview returns a short sample of the SQL for a full config.
func (s *FileService) CsvToSQLConfigPreview(fileID string, cfg CsvSqlConfig) (string, error) {
	r, _, err := s.csvSampleReader(fileID)
	if err != nil {
		return "", err
	}
	opts, err := csvSqlOptions(cfg)
	if err != nil {
		return "", err
	}
	rep, err := csv.PreviewSQLConversionContext(context.Background(), r, csv.SQLPreviewOptions{
		SQLConvertOptions: opts,
		MaxBytes:          csvSampleBytes,
		MaxRows:           20,
	})
	if err != nil {
		return "", err
	}
	return rep.SQL, nil
}

// CsvToSQLConfigViaDialog streams the whole CSV to a .sql file using a full
// column-mapping config (select/rename/type, insert mode, batch, null, policy).
func (s *FileService) CsvToSQLConfigViaDialog(fileID string, cfg CsvSqlConfig) (TransformResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	opts, err := csvSqlOptions(cfg)
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := saveDialog("Save SQL as", sqlTableName(cfg.TableName)+".sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	sum, err := csv.ConvertToSQLFile(context.Background(), f.Path, dst, opts)
	if err != nil {
		return TransformResult{}, err
	}
	note := fmt.Sprintf("table %q · %d rows", sum.TableName, sum.RowsWritten)
	if sum.SkippedRows > 0 || sum.SanitizedRows > 0 {
		note += fmt.Sprintf(" (%d skipped, %d sanitized)", sum.SkippedRows, sum.SanitizedRows)
	}
	return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RowsWritten, Note: note}, nil
}
