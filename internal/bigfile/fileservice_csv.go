package bigfile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/plugins/csv"
	"novera/internal/bigfile/session"
)

const (
	csvSampleBytes        int64 = 2 << 20 // raw source bytes for inspect/schema/preview
	csvSampleDecodedBytes int64 = 3 * csvSampleBytes

	csvGridDefaultRawBytes = 128 * 1024
	csvGridMaxRawBytes     = maxCSVGridBytes
	csvGridMaxRows         = 500
	csvGridMaxColumns      = 256
	csvGridMaxCells        = 10_000
)

var (
	ErrCSVTransformEncodingUnsupported = errors.New("CSV transform source encoding is unsupported")
	ErrCSVSourceGenerationRequired     = errors.New("CSV source generation is required")
)

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
	r, size := utf8.DecodeRuneInString(s)
	if (r == utf8.RuneError && size == 1) || size != len(s) {
		// Zero means "use comma" in the CSV engines. Return an explicitly
		// invalid nonzero rune so malformed/multi-rune requests fail closed.
		return utf8.RuneError
	}
	if size == 0 {
		return ','
	}
	return r
}

func delimiterString(r rune) string {
	if r == 0 {
		return ","
	}
	return string(r)
}

func parseCSVDelimiter(value string) (rune, error) {
	delimiter := delimiterRune(value)
	if err := csv.ValidateDelimiter(delimiter); err != nil {
		return 0, err
	}
	return delimiter, nil
}

type leasedSampleReader struct {
	io.Reader
	release func()
}

func (r *leasedSampleReader) Close() error {
	if r != nil && r.release != nil {
		r.release()
		r.release = nil
	}
	return nil
}

func (s *FileService) csvSampleReader(fileID string) (io.ReadCloser, string, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return nil, "", fmt.Errorf("unknown file id %q", fileID)
	}
	n := f.Doc.Size()
	// CSV samplers need one byte beyond their advertised payload budget to
	// distinguish an exact-size source from truncation and avoid accepting a
	// record cut at the sample boundary.
	if n > csvSampleBytes+1 {
		n = csvSampleBytes + 1
	}
	return &leasedSampleReader{Reader: io.NewSectionReader(f.Doc, 0, n), release: f.Release}, f.Path, nil
}

type csvDecodedSample struct {
	data       []byte
	encoding   string
	truncated  bool
	generation uint64
}

func (s *FileService) csvSample(fileID string) (csvDecodedSample, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return csvDecodedSample{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	meta := f.Doc.Metadata()
	if meta.EncodingRequiresConfirmation {
		return csvDecodedSample{}, fmt.Errorf("%w: %s", encodingx.ErrEncodingConfirmationRequired, meta.Encoding)
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return csvDecodedSample{}, err
	}
	decoded, err := csv.DecodeSourceWindow(context.Background(), io.NewSectionReader(f.Doc, 0, f.Doc.Size()), csv.DecodeWindowOptions{
		Encoding:        meta.Encoding,
		AtBOF:           true,
		MaxRawBytes:     csvSampleBytes,
		MaxDecodedBytes: csvSampleDecodedBytes,
	})
	if err != nil {
		return csvDecodedSample{}, err
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return csvDecodedSample{}, err
	}
	return csvDecodedSample{
		data: decoded.Data, encoding: meta.Encoding, truncated: decoded.Truncated, generation: f.Generation,
	}, nil
}

func prepareCSVSample(ctx context.Context, sample csvDecodedSample, delimiter rune) ([]byte, []string, error) {
	data, omitted, err := csv.CompleteRecordPrefix(ctx, sample.data, delimiter, csvSampleDecodedBytes, sample.truncated)
	if err != nil {
		return nil, nil, err
	}
	warnings := csvSampleWarnings(sample)
	if omitted {
		if len(data) == 0 && len(sample.data) > 0 {
			return nil, nil, fmt.Errorf("the first CSV logical record exceeds the %d-byte raw preview budget", csvSampleBytes)
		}
		warnings = append(warnings, "trailing partial logical record omitted from the bounded sample")
	}
	return data, warnings, nil
}

func csvSampleWarnings(sample csvDecodedSample) []string {
	if !sample.truncated {
		return nil
	}
	return []string{fmt.Sprintf("source sample limited to %d raw bytes and decoded as %s", csvSampleBytes, sample.encoding)}
}

func decodedSampleLimit(data []byte) int64 {
	if len(data) == 0 {
		return 1
	}
	return int64(len(data))
}

// ---- inspect (delimiter detection) -------------------------------------

type CsvDelimiterOption struct {
	Delimiter string  `json:"delimiter"`
	Name      string  `json:"name"`
	Columns   int     `json:"columns"`
	Score     float64 `json:"score"`
}

type CsvInspectResult struct {
	Generation    uint64               `json:"generation"`
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
	sample, err := s.csvSample(fileID)
	if err != nil {
		return CsvInspectResult{}, err
	}
	rep, err := csv.InspectReaderContext(context.Background(), bytes.NewReader(sample.data), csv.InspectOptions{
		MaxBytes: decodedSampleLimit(sample.data), MaxRows: 1000, SourceTruncated: sample.truncated,
	})
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
		Generation:    sample.generation,
		Delimiter:     delimiterString(rep.Delimiter),
		DelimiterName: rep.DelimiterName,
		Confidence:    rep.Confidence,
		Columns:       rep.Columns,
		HasHeader:     rep.HasHeader,
		Candidates:    cands,
		Warnings:      append(rep.Warnings, csvSampleWarnings(sample)...),
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
	Generation uint64      `json:"generation"`
	Columns    []CsvColumn `json:"columns"`
	HasHeader  bool        `json:"hasHeader"`
	Warnings   []string    `json:"warnings"`
}

func (s *FileService) CsvSchema(fileID string, delimiter string, hasHeader bool) (CsvSchemaResult, error) {
	parsedDelimiter, err := parseCSVDelimiter(delimiter)
	if err != nil {
		return CsvSchemaResult{}, err
	}
	sample, err := s.csvSample(fileID)
	if err != nil {
		return CsvSchemaResult{}, err
	}
	data, warnings, err := prepareCSVSample(context.Background(), sample, parsedDelimiter)
	if err != nil {
		return CsvSchemaResult{}, err
	}
	rep, err := csv.InferSchemaContext(context.Background(), bytes.NewReader(data), csv.SchemaOptions{
		Delimiter: parsedDelimiter,
		HasHeader: hasHeader,
		MaxBytes:  decodedSampleLimit(data),
		MaxRows:   2000,
	})
	if err != nil {
		return CsvSchemaResult{}, err
	}
	cols := make([]CsvColumn, 0, len(rep.Columns))
	for _, c := range rep.Columns {
		cols = append(cols, CsvColumn{Name: c.Name, SQLType: c.SQLType, NonNull: c.NonNullCount, Null: c.NullCount, Samples: c.SampleValues})
	}
	return CsvSchemaResult{
		Generation: sample.generation,
		Columns:    cols,
		HasHeader:  rep.HasHeader,
		Warnings:   append(rep.Warnings, warnings...),
	}, nil
}

type CsvPreviewResult struct {
	Generation uint64     `json:"generation"`
	Header     []string   `json:"header"`
	Rows       [][]string `json:"rows"`
	Warnings   []string   `json:"warnings"`
}

func (s *FileService) CsvPreview(fileID string, delimiter string, hasHeader bool, maxRows int) (CsvPreviewResult, error) {
	parsedDelimiter, err := parseCSVDelimiter(delimiter)
	if err != nil {
		return CsvPreviewResult{}, err
	}
	maxRows = clampRequestInt(maxRows, defaultCSVPreviewRows, maxCSVPreviewRows)
	sample, err := s.csvSample(fileID)
	if err != nil {
		return CsvPreviewResult{}, err
	}
	data, warnings, err := prepareCSVSample(context.Background(), sample, parsedDelimiter)
	if err != nil {
		return CsvPreviewResult{}, err
	}
	rep, err := csv.PreviewRowsContext(context.Background(), bytes.NewReader(data), csv.PreviewOptions{
		Delimiter: parsedDelimiter,
		HasHeader: hasHeader,
		MaxBytes:  decodedSampleLimit(data),
		MaxRows:   maxRows,
	})
	if err != nil {
		return CsvPreviewResult{}, err
	}
	return CsvPreviewResult{
		Generation: sample.generation,
		Header:     rep.Header,
		Rows:       rep.Rows,
		Warnings:   append(rep.Warnings, warnings...),
	}, nil
}

// ---- windowed grid (spreadsheet view) -----------------------------------

type CsvGridResult struct {
	FileID       string     `json:"fileId"`
	Generation   uint64     `json:"generation"`
	Encoding     string     `json:"encoding"`
	StartByte    int64      `json:"startByte"`
	NextByte     int64      `json:"nextByte"`
	StartRow     int64      `json:"startRow"` // approximate physical line of the first record
	Rows         [][]string `json:"rows"`
	Columns      int        `json:"columns"`
	SourceBytes  int64      `json:"sourceBytes"`
	DecodedBytes int        `json:"decodedBytes"`
	AtBof        bool       `json:"atBof"`
	AtEof        bool       `json:"atEof"`
}

// GetCsvGrid parses a bounded raw-byte window into complete logical CSV
// records. startByte must be zero or a NextByte returned by an earlier call for
// the same file generation and delimiter. Physical newlines inside quoted
// fields never become cursors.
func (s *FileService) GetCsvGrid(fileID string, delimiter string, startByte int64, maxBytes int) (CsvGridResult, error) {
	parsedDelimiter, err := parseCSVDelimiter(delimiter)
	if err != nil {
		return CsvGridResult{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return CsvGridResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if maxBytes < 0 {
		return CsvGridResult{}, errors.New("CSV grid raw-byte budget must not be negative")
	}
	if maxBytes == 0 {
		maxBytes = csvGridDefaultRawBytes
	}
	if maxBytes > csvGridMaxRawBytes {
		return CsvGridResult{}, fmt.Errorf("CSV grid raw-byte budget %d exceeds hard limit %d", maxBytes, csvGridMaxRawBytes)
	}
	meta := f.Doc.Metadata()
	if meta.EncodingRequiresConfirmation {
		return CsvGridResult{}, fmt.Errorf("%w: %s", encodingx.ErrEncodingConfirmationRequired, meta.Encoding)
	}
	size := f.Doc.Size()
	if startByte < 0 || startByte > size {
		return CsvGridResult{}, fmt.Errorf("CSV grid cursor %d is outside [0, %d]", startByte, size)
	}
	if err := s.csvCursors.validate(fileID, f.Generation, parsedDelimiter, startByte); err != nil {
		return CsvGridResult{}, err
	}
	aligned, err := f.Doc.AlignTextOffsetBackward(startByte)
	if err != nil {
		return CsvGridResult{}, err
	}
	if aligned != startByte {
		return CsvGridResult{}, fmt.Errorf("CSV grid cursor %d splits a %s source character", startByte, meta.Encoding)
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return CsvGridResult{}, err
	}
	decoded, err := csv.DecodeSourceWindow(context.Background(), io.NewSectionReader(f.Doc, startByte, size-startByte), csv.DecodeWindowOptions{
		Encoding:        meta.Encoding,
		AtBOF:           startByte == 0,
		MaxRawBytes:     int64(maxBytes),
		MaxDecodedBytes: int64(3 * maxBytes),
		TrackOffsets:    true,
	})
	if err != nil {
		return CsvGridResult{}, err
	}
	records, err := csv.ParseRecordWindow(context.Background(), decoded.Data, csv.RecordWindowOptions{
		Delimiter:       parsedDelimiter,
		SourceTruncated: decoded.Truncated,
		MaxRecordBytes:  int64(3 * maxBytes),
		MaxRows:         csvGridMaxRows,
		MaxColumns:      csvGridMaxColumns,
		MaxCells:        csvGridMaxCells,
	})
	if err != nil {
		return CsvGridResult{}, err
	}
	rawEnd, err := decoded.RawOffsetForDecodedEnd(records.DecodedEnd)
	if err != nil {
		return CsvGridResult{}, err
	}
	if records.DecodedEnd == 0 && len(decoded.Data) == 0 && !decoded.Truncated {
		rawEnd = decoded.RawBytesDecoded
	}
	nextByte := startByte + rawEnd
	if nextByte == startByte && startByte < size {
		return CsvGridResult{}, errors.New("CSV grid raw-byte budget is too small for one complete source character or logical record")
	}
	if nextByte > size {
		return CsvGridResult{}, errors.New("CSV parser continuation exceeds the opened source size")
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return CsvGridResult{}, err
	}
	s.csvCursors.issue(fileID, f.Generation, parsedDelimiter, nextByte)
	startRow, _ := f.Doc.ApproxOffsetToLine(startByte)
	if startRow <= 0 {
		startRow = 1
	}
	return CsvGridResult{
		FileID:       fileID,
		Generation:   f.Generation,
		Encoding:     meta.Encoding,
		StartByte:    startByte,
		NextByte:     nextByte,
		StartRow:     startRow,
		Rows:         records.Rows,
		Columns:      records.Columns,
		SourceBytes:  nextByte - startByte,
		DecodedBytes: records.DecodedEnd,
		AtBof:        startByte == 0,
		AtEof:        nextByte >= size,
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

// csvTransformSaveDialog is a test seam for non-SQL CSV transforms. Keeping it
// separate from SQL dialog seams prevents unrelated service tests from racing.
var csvTransformSaveDialog = saveDialog

// runCSVTransformVerified binds a streamed transform to the exact retained
// document generation. Each source block is authenticated before its bytes are
// released, the complete source is revalidated before publication, and the
// final path appears only through no-clobber atomic publication.
func runCSVTransformVerified[T any](
	ctx context.Context,
	doc *document.FileDocument,
	sourcePath string,
	destination string,
	progress func(int64, string),
	transform func(context.Context, io.Reader, io.Writer) (T, error),
) (_ T, retErr error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	if transform == nil {
		return zero, errors.New("CSV transform is required")
	}
	expected, err := captureDocumentSourceExpectation(ctx, doc, sourcePath, func(completed, _ int64) {
		if progress != nil {
			progress(completed, "verifying source")
		}
	})
	if err != nil {
		return zero, err
	}
	verified, err := newVerifiedDocumentReader(ctx, expected, doc)
	if err != nil {
		return zero, err
	}
	output, err := fileio.OpenAtomicOutput(destination, []string{sourcePath}, 0o600)
	if err != nil {
		return zero, err
	}
	defer func() { retErr = errors.Join(retErr, output.Cleanup()) }()

	reader := io.NewSectionReader(verified, 0, verified.Size())
	result, err := transform(ctx, reader, output)
	if err != nil {
		return result, err
	}
	if err := expected.validateContext(ctx, doc, func(completed, _ int64) {
		if progress != nil {
			progress(completed, "revalidating source")
		}
	}); err != nil {
		return result, err
	}
	if err := output.CommitContext(ctx); err != nil {
		return result, err
	}
	return result, nil
}

func validateCSVSourceGeneration(file *session.File, expectedGeneration uint64) error {
	if expectedGeneration == 0 {
		return ErrCSVSourceGenerationRequired
	}
	if file == nil || file.Generation != expectedGeneration {
		current := uint64(0)
		if file != nil {
			current = file.Generation
		}
		return fmt.Errorf(
			"%w: CSV preview generation %d is no longer current (current generation %d)",
			ErrOutputSourceChanged,
			expectedGeneration,
			current,
		)
	}
	return nil
}

func requireCSVTransformEncoding(file *session.File) error {
	if file == nil || file.Doc == nil {
		return errors.New("CSV source document is required")
	}
	meta := file.Doc.Metadata()
	if meta.EncodingRequiresConfirmation {
		return fmt.Errorf("%w: choose the source encoding explicitly before transforming", encodingx.ErrEncodingConfirmationRequired)
	}
	if meta.Encoding != "" &&
		!strings.EqualFold(meta.Encoding, "UTF-8") &&
		!strings.EqualFold(meta.Encoding, "ASCII") &&
		!strings.EqualFold(meta.Encoding, "US-ASCII") {
		return fmt.Errorf(
			"%w: %s input must be converted to UTF-8 before CSV transforms so output semantics are explicit",
			ErrCSVTransformEncodingUnsupported,
			meta.Encoding,
		)
	}
	if err := file.Doc.ValidateUnchanged(); err != nil {
		return fmt.Errorf("%w: CSV source generation changed: %v", ErrOutputSourceChanged, err)
	}
	return nil
}

// csvTransformPreflight validates the caller's analysis token and source
// encoding before a native save dialog can appear. The lease is intentionally
// released before the dialog; the transform job reacquires and revalidates the
// exact generation afterward.
func (s *FileService) csvTransformPreflight(fileID string, expectedGeneration uint64) error {
	file, ok := s.reg.Get(fileID)
	if !ok {
		return fmt.Errorf("unknown file id %q", fileID)
	}
	defer file.Release()
	if err := validateCSVSourceGeneration(file, expectedGeneration); err != nil {
		return err
	}
	return requireCSVTransformEncoding(file)
}

func (s *FileService) withCSVTransformJob(
	fileID string,
	expectedGeneration uint64,
	title string,
	fn func(context.Context, *session.File, func(int64, string)) (TransformResult, error),
) (TransformResult, error) {
	return s.withFileJob(fileID, title, func(ctx context.Context, progress func(int64, string)) (TransformResult, error) {
		file, ok := s.reg.Get(fileID)
		if !ok {
			return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
		}
		defer file.Release()
		if err := validateCSVSourceGeneration(file, expectedGeneration); err != nil {
			return TransformResult{}, err
		}
		if err := requireCSVTransformEncoding(file); err != nil {
			return TransformResult{}, err
		}
		return fn(ctx, file, progress)
	})
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
	Generation     uint64              `json:"generation"`
	Columns        []csv.ColumnProfile `json:"columns"`
	RecordsScanned int                 `json:"recordsScanned"`
	RaggedRows     int                 `json:"raggedRows"`
	Truncated      bool                `json:"truncated"`
}

// CsvProfile profiles each column (null %, distinct, min/max, top values) and
// counts ragged rows over a bounded sample.
func (s *FileService) CsvProfile(fileID, delimiter string, hasHeader bool) (CsvProfileResult, error) {
	parsedDelimiter, err := parseCSVDelimiter(delimiter)
	if err != nil {
		return CsvProfileResult{}, err
	}
	sample, err := s.csvSample(fileID)
	if err != nil {
		return CsvProfileResult{}, err
	}
	data, _, err := prepareCSVSample(context.Background(), sample, parsedDelimiter)
	if err != nil {
		return CsvProfileResult{}, err
	}
	rep, err := csv.ProfileColumns(context.Background(), bytes.NewReader(data), csv.SchemaOptions{
		Delimiter:  parsedDelimiter,
		HasHeader:  hasHeader,
		MaxBytes:   decodedSampleLimit(data),
		NullValues: []string{"", "NULL", "null", "\\N"},
	})
	if err != nil {
		return CsvProfileResult{}, err
	}
	return CsvProfileResult{
		Generation:     sample.generation,
		Columns:        rep.Columns,
		RecordsScanned: rep.RecordsScanned,
		RaggedRows:     rep.RaggedRows,
		Truncated:      rep.TruncatedSample || sample.truncated,
	}, nil
}

// CsvProjectViaDialog writes a new CSV keeping only keepIndices (0-based) in the
// given order — used for drop-column and reorder.
func (s *FileService) CsvProjectViaDialog(fileID string, sourceGeneration uint64, delimiter string, keepIndices []int) (TransformResult, error) {
	opts := csv.ProjectOptions{
		Delimiter: delimiterRune(delimiter),
		Columns:   append([]int(nil), keepIndices...),
	}
	if err := csv.ValidateProjectOptions(opts); err != nil {
		return TransformResult{}, err
	}
	if err := s.csvTransformPreflight(fileID, sourceGeneration); err != nil {
		return TransformResult{}, err
	}
	dst, err := csvTransformSaveDialog("Save projected CSV as", "projected.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withCSVTransformJob(fileID, sourceGeneration, "Project CSV", func(ctx context.Context, file *session.File, progress func(int64, string)) (TransformResult, error) {
		jobOpts := opts
		jobOpts.Progress = func(p csv.ProjectProgress) { progress(p.RecordsRead, "rows read") }
		sum, err := runCSVTransformVerified(ctx, file.Doc, file.Path, dst, progress, func(ctx context.Context, r io.Reader, w io.Writer) (csv.ProjectSummary, error) {
			return csv.ProjectColumns(ctx, r, w, jobOpts)
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RecordsWritten, Note: fmt.Sprintf("%d columns kept", sum.ColumnsWritten)}, nil
	})
}

// CsvAddColumnViaDialog writes a new CSV with a constant column appended to every
// row (including the header, which becomes the value).
func (s *FileService) CsvAddColumnViaDialog(fileID string, sourceGeneration uint64, delimiter string, columnCount int, value string) (TransformResult, error) {
	opts := csv.AddColumnOptions{
		Delimiter: delimiterRune(delimiter),
		Position:  columnCount,
		Value:     value,
	}
	if err := csv.ValidateAddColumnOptions(opts); err != nil {
		return TransformResult{}, err
	}
	if err := s.csvTransformPreflight(fileID, sourceGeneration); err != nil {
		return TransformResult{}, err
	}
	dst, err := csvTransformSaveDialog("Save CSV with added column as", "with-column.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withCSVTransformJob(fileID, sourceGeneration, "Add CSV column", func(ctx context.Context, file *session.File, progress func(int64, string)) (TransformResult, error) {
		jobOpts := opts
		jobOpts.Progress = func(p csv.AddColumnProgress) { progress(p.RecordsRead, "rows read") }
		sum, err := runCSVTransformVerified(ctx, file.Doc, file.Path, dst, progress, func(ctx context.Context, r io.Reader, w io.Writer) (csv.AddColumnSummary, error) {
			return csv.AddColumn(ctx, r, w, jobOpts)
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RecordsWritten, Note: "constant column added"}, nil
	})
}

// CsvTextPreviewResult binds rendered preview text to the exact open session
// generation that supplied its bytes. Callers must echo that generation in a
// later artifact request.
type CsvTextPreviewResult struct {
	Generation uint64 `json:"generation"`
	Text       string `json:"text"`
}

// CsvToSQLPreview returns a short sample of the generated SQL.
func (s *FileService) CsvToSQLPreview(fileID, delimiter, tableName string, hasHeader, includeCreate bool) (CsvTextPreviewResult, error) {
	opts, err := csvSimpleSQLOptions(delimiter, tableName, hasHeader, includeCreate)
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	sample, err := s.csvSample(fileID)
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	data, _, err := prepareCSVSample(context.Background(), sample, opts.Delimiter)
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	rep, err := csv.PreviewSQLConversionContext(context.Background(), bytes.NewReader(data), csv.SQLPreviewOptions{
		SQLConvertOptions: opts,
		MaxBytes:          decodedSampleLimit(data),
		MaxRows:           20,
	})
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	return CsvTextPreviewResult{Generation: sample.generation, Text: rep.SQL}, nil
}

// CsvToSQLViaDialog streams the whole CSV to a .sql file of INSERTs.
func (s *FileService) CsvToSQLViaDialog(fileID string, sourceGeneration uint64, delimiter, tableName string, hasHeader, includeCreate bool) (TransformResult, error) {
	opts, err := csvSimpleSQLOptions(delimiter, tableName, hasHeader, includeCreate)
	if err != nil {
		return TransformResult{}, err
	}
	if err := s.csvTransformPreflight(fileID, sourceGeneration); err != nil {
		return TransformResult{}, err
	}
	dst, err := csvTransformSaveDialog("Save SQL as", opts.TableName+".sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withCSVTransformJob(fileID, sourceGeneration, "Convert CSV to SQL", func(ctx context.Context, file *session.File, progress func(int64, string)) (TransformResult, error) {
		jobOpts := opts
		jobOpts.Progress = func(p csv.SQLConvertProgress) { progress(p.RecordsRead, "rows read") }
		sum, err := convertCSVToSQLVerified(ctx, file.Doc, file.Path, dst, jobOpts, func(completed int64, phase string) {
			progress(completed, phase)
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RowsWritten, Note: fmt.Sprintf("table %q", sum.TableName)}, nil
	})
}

func csvSimpleSQLOptions(delimiter, tableName string, hasHeader, includeCreate bool) (csv.SQLConvertOptions, error) {
	if len(tableName) > csv.MaxSQLIdentifierBytes {
		return csv.SQLConvertOptions{}, fmt.Errorf("table name exceeds the %d-byte SQL identifier input limit", csv.MaxSQLIdentifierBytes)
	}
	parsedDelimiter, err := csvSQLDelimiter(delimiter)
	if err != nil {
		return csv.SQLConvertOptions{}, err
	}
	opts := csv.SQLConvertOptions{
		Delimiter:          parsedDelimiter,
		TableName:          sqlTableName(tableName),
		HasHeader:          hasHeader,
		IncludeCreateTable: includeCreate,
	}
	if err := csv.ValidateSQLConvertOptions(opts); err != nil {
		return csv.SQLConvertOptions{}, err
	}
	return opts, nil
}

func csvSQLDelimiter(value string) (rune, error) {
	if value == "" {
		return 0, errors.New("CSV-to-SQL delimiter is required")
	}
	delimiter := delimiterRune(value)
	if err := csv.ValidateDelimiter(delimiter); err != nil {
		return 0, err
	}
	return delimiter, nil
}

func convertCSVToSQLVerified(ctx context.Context, doc *document.FileDocument, sourcePath, destination string, opts csv.SQLConvertOptions, progress func(int64, string)) (_ csv.SQLConvertSummary, retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	expected, err := captureDocumentSourceExpectation(ctx, doc, sourcePath, func(completed, _ int64) {
		if progress != nil {
			progress(completed, "verifying source")
		}
	})
	if err != nil {
		return csv.SQLConvertSummary{}, err
	}
	verified, err := newVerifiedDocumentReader(ctx, expected, doc)
	if err != nil {
		return csv.SQLConvertSummary{}, err
	}
	output, err := fileio.OpenAtomicOutput(destination, []string{sourcePath}, 0o600)
	if err != nil {
		return csv.SQLConvertSummary{}, err
	}
	defer func() {
		retErr = errors.Join(retErr, output.Cleanup())
	}()

	summary, err := csv.ConvertToSQLReaderAt(ctx, verified, verified.Size(), output, opts)
	if err != nil {
		return summary, err
	}
	if err := expected.validateContext(ctx, doc, func(completed, _ int64) {
		if progress != nil {
			progress(completed, "revalidating source")
		}
	}); err != nil {
		return summary, err
	}
	if err := output.CommitContext(ctx); err != nil {
		return summary, err
	}
	return summary, nil
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
	Type    string `json:"type"`    // SQL type; BOOLEAN also controls value validation/emission
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
	if err := validateCsvSqlConfigCollections(cfg); err != nil {
		return csv.SQLConvertOptions{}, err
	}
	parsedDelimiter, err := csvSQLDelimiter(cfg.Delimiter)
	if err != nil {
		return csv.SQLConvertOptions{}, err
	}
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
	var insertMode csv.SQLInsertMode
	switch cfg.InsertMode {
	case "insert":
		insertMode = csv.SQLInsertModeInsert
	case "ignore":
		insertMode = csv.SQLInsertModeInsertIgnore
	case "replace":
		insertMode = csv.SQLInsertModeReplace
	default:
		return csv.SQLConvertOptions{}, fmt.Errorf("invalid CSV-to-SQL insert mode %q", cfg.InsertMode)
	}
	opts := csv.SQLConvertOptions{
		Delimiter:          parsedDelimiter,
		TableName:          sqlTableName(cfg.TableName),
		HasHeader:          cfg.HasHeader,
		Columns:            names,
		ColumnTypes:        types,
		SourceColumns:      src,
		IncludeCreateTable: cfg.IncludeCreate,
		InsertMode:         insertMode,
		InsertBatchSize:    cfg.BatchSize,
		NullValues:         cfg.NullValues,
		OnInvalidValue:     csv.InvalidValuePolicy(cfg.OnInvalid),
	}
	if err := csv.ValidateSQLConvertOptions(opts); err != nil {
		return csv.SQLConvertOptions{}, err
	}
	return opts, nil
}

func addCSVTransformConfigString(total *int, label, value string) error {
	remaining := csv.MaxTransformConfigStringBytes - *total
	if len(value) > remaining {
		return fmt.Errorf("%s configuration strings exceed %d-byte aggregate limit", label, csv.MaxTransformConfigStringBytes)
	}
	*total += len(value)
	return nil
}

func validateCsvSqlConfigCollections(cfg CsvSqlConfig) error {
	if len(cfg.Columns) > csv.MaxTransformColumnMappings {
		return fmt.Errorf("CSV-to-SQL config has %d column mappings; maximum is %d", len(cfg.Columns), csv.MaxTransformColumnMappings)
	}
	if len(cfg.NullValues) > csv.MaxTransformColumnMappings {
		return fmt.Errorf("CSV-to-SQL config has %d null sentinels; maximum is %d", len(cfg.NullValues), csv.MaxTransformColumnMappings)
	}
	stringBytes := 0
	for _, value := range []string{cfg.Delimiter, cfg.TableName, cfg.InsertMode, cfg.OnInvalid} {
		if err := addCSVTransformConfigString(&stringBytes, "CSV-to-SQL", value); err != nil {
			return err
		}
	}
	for i, column := range cfg.Columns {
		if column.Source < 0 {
			return fmt.Errorf("CSV-to-SQL source column %d at position %d is negative", column.Source, i)
		}
		for previous := 0; previous < i; previous++ {
			if cfg.Columns[previous].Source == column.Source {
				return fmt.Errorf("CSV-to-SQL source column %d is selected more than once", column.Source)
			}
		}
		if err := addCSVTransformConfigString(&stringBytes, "CSV-to-SQL", column.Name); err != nil {
			return err
		}
		if err := addCSVTransformConfigString(&stringBytes, "CSV-to-SQL", column.Type); err != nil {
			return err
		}
	}
	for _, value := range cfg.NullValues {
		if err := addCSVTransformConfigString(&stringBytes, "CSV-to-SQL", value); err != nil {
			return err
		}
	}
	return nil
}

// CsvRedactColumn is one column to mask in a redaction.
type CsvRedactColumn struct {
	Index int    `json:"index"`
	Mode  string `json:"mode"` // null | fixed | hash | email
}

// CsvRedactViaDialog writes an anonymized copy of the CSV with the chosen columns
// masked, for sharing a dataset without leaking PII. Source is never modified.
func (s *FileService) CsvRedactViaDialog(fileID string, sourceGeneration uint64, delimiter string, hasHeader bool, columns []CsvRedactColumn, replacement string) (TransformResult, error) {
	if len(columns) > csv.MaxTransformColumnMappings {
		return TransformResult{}, fmt.Errorf("CSV redaction has %d column mappings; maximum is %d", len(columns), csv.MaxTransformColumnMappings)
	}
	cols := make(map[int]csv.RedactMode, len(columns))
	usesHash := false
	for _, column := range columns {
		if _, duplicate := cols[column.Index]; duplicate {
			return TransformResult{}, fmt.Errorf("CSV redaction column index %d is selected more than once", column.Index)
		}
		mode := csv.RedactMode(column.Mode)
		cols[column.Index] = mode
		usesHash = usesHash || mode == csv.RedactHash
	}
	var pseudonymKey []byte
	var err error
	if usesHash {
		pseudonymKey, err = csv.NewPseudonymKey()
		if err != nil {
			return TransformResult{}, err
		}
		defer clear(pseudonymKey)
	}
	opts := csv.RedactOptions{
		Delimiter:    delimiterRune(delimiter),
		HasHeader:    hasHeader,
		Columns:      cols,
		Replacement:  replacement,
		PseudonymKey: pseudonymKey,
	}
	if err := csv.ValidateRedactOptions(opts); err != nil {
		return TransformResult{}, err
	}
	if err := s.csvTransformPreflight(fileID, sourceGeneration); err != nil {
		return TransformResult{}, err
	}
	dst, err := csvTransformSaveDialog("Save redacted copy as", "redacted.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withCSVTransformJob(fileID, sourceGeneration, "Redact CSV", func(ctx context.Context, file *session.File, progress func(int64, string)) (TransformResult, error) {
		jobOpts := opts
		jobOpts.Progress = func(r int64, _ int64) { progress(r, "rows read") }
		sum, err := runCSVTransformVerified(ctx, file.Doc, file.Path, dst, progress, func(ctx context.Context, r io.Reader, w io.Writer) (csv.RedactSummary, error) {
			return csv.RedactColumns(ctx, r, w, jobOpts)
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
func (s *FileService) CsvFilterViaDialog(fileID string, sourceGeneration uint64, delimiter string, hasHeader bool, column int, op, value string, negate bool) (TransformResult, error) {
	opts := csv.FilterOptions{
		Delimiter: delimiterRune(delimiter),
		HasHeader: hasHeader,
		Column:    column,
		Op:        op,
		Value:     value,
		Negate:    negate,
	}
	if err := csv.ValidateFilterOptions(opts); err != nil {
		return TransformResult{}, err
	}
	if err := s.csvTransformPreflight(fileID, sourceGeneration); err != nil {
		return TransformResult{}, err
	}
	dst, err := csvTransformSaveDialog("Save filtered CSV as", "filtered.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withCSVTransformJob(fileID, sourceGeneration, "Filter CSV", func(ctx context.Context, file *session.File, progress func(int64, string)) (TransformResult, error) {
		jobOpts := opts
		jobOpts.Progress = func(r int64) { progress(r, "rows read") }
		sum, err := runCSVTransformVerified(ctx, file.Doc, file.Path, dst, progress, func(ctx context.Context, r io.Reader, w io.Writer) (csv.TransformSummary, error) {
			return csv.FilterRows(ctx, r, w, jobOpts)
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
func (s *FileService) CsvDedupeViaDialog(fileID string, sourceGeneration uint64, delimiter string, hasHeader bool, keyColumn int) (TransformResult, error) {
	opts := csv.DedupeOptions{
		Delimiter: delimiterRune(delimiter),
		HasHeader: hasHeader,
		KeyColumn: keyColumn,
	}
	if err := csv.ValidateDedupeOptions(opts); err != nil {
		return TransformResult{}, err
	}
	if err := s.csvTransformPreflight(fileID, sourceGeneration); err != nil {
		return TransformResult{}, err
	}
	dst, err := csvTransformSaveDialog("Save deduplicated CSV as", "deduped.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withCSVTransformJob(fileID, sourceGeneration, "Deduplicate CSV", func(ctx context.Context, file *session.File, progress func(int64, string)) (TransformResult, error) {
		jobOpts := opts
		jobOpts.Progress = func(r int64) { progress(r, "rows read") }
		sum, err := runCSVTransformVerified(ctx, file.Doc, file.Path, dst, progress, func(ctx context.Context, r io.Reader, w io.Writer) (csv.TransformSummary, error) {
			return csv.DedupeRows(ctx, r, w, jobOpts)
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
func (s *FileService) CsvSampleViaDialog(fileID string, sourceGeneration uint64, delimiter string, hasHeader bool, everyN int) (TransformResult, error) {
	if everyN <= 0 {
		everyN = 10
	}
	opts := csv.SampleOptions{
		Delimiter: delimiterRune(delimiter),
		HasHeader: hasHeader,
		EveryN:    everyN,
	}
	if err := csv.ValidateSampleOptions(opts); err != nil {
		return TransformResult{}, err
	}
	if err := s.csvTransformPreflight(fileID, sourceGeneration); err != nil {
		return TransformResult{}, err
	}
	dst, err := csvTransformSaveDialog("Save sampled CSV as", "sampled.csv")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withCSVTransformJob(fileID, sourceGeneration, "Sample CSV", func(ctx context.Context, file *session.File, progress func(int64, string)) (TransformResult, error) {
		jobOpts := opts
		jobOpts.Progress = func(r int64) { progress(r, "rows read") }
		sum, err := runCSVTransformVerified(ctx, file.Doc, file.Path, dst, progress, func(ctx context.Context, r io.Reader, w io.Writer) (csv.TransformSummary, error) {
			return csv.SampleRows(ctx, r, w, jobOpts)
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
func (s *FileService) CsvExportJSONLViaDialog(fileID string, sourceGeneration uint64, delimiter string, hasHeader, numberKeys bool) (TransformResult, error) {
	opts := csv.JSONLOptions{
		Delimiter:  delimiterRune(delimiter),
		HasHeader:  hasHeader,
		NumberKeys: numberKeys,
	}
	if err := csv.ValidateJSONLOptions(opts); err != nil {
		return TransformResult{}, err
	}
	if err := s.csvTransformPreflight(fileID, sourceGeneration); err != nil {
		return TransformResult{}, err
	}
	dst, err := csvTransformSaveDialog("Export JSON Lines as", "export.jsonl")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withCSVTransformJob(fileID, sourceGeneration, "Export JSONL", func(ctx context.Context, file *session.File, progress func(int64, string)) (TransformResult, error) {
		jobOpts := opts
		jobOpts.Progress = func(r int64) { progress(r, "rows read") }
		sum, err := runCSVTransformVerified(ctx, file.Doc, file.Path, dst, progress, func(ctx context.Context, r io.Reader, w io.Writer) (csv.ExportSummary, error) {
			return csv.ExportJSONL(ctx, r, w, jobOpts)
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RecordsWritten, Note: "JSON Lines"}, nil
	})
}

// CsvExportSQLiteViaDialog is disabled until SQLite publication can provide
// the same atomic, no-overwrite guarantees as the other artifact transforms.
func (s *FileService) CsvExportSQLiteViaDialog(fileID string, sourceGeneration uint64, delimiter string, hasHeader bool, tableName string, typedCells bool) (TransformResult, error) {
	return TransformResult{}, csv.ErrSQLiteExportSecurePublicationUnavailable
}

// CsvExportXLSXViaDialog is disabled until workbook generation can use secure,
// exclusively owned scratch storage.
func (s *FileService) CsvExportXLSXViaDialog(fileID string, sourceGeneration uint64, delimiter string, hasHeader bool, sheetName string, typedCells bool) (TransformResult, error) {
	return TransformResult{}, csv.ErrXLSXExportSecureScratchUnavailable
}

// CsvMarkdownPreview returns the current preview as a Markdown table (for copy).
func (s *FileService) CsvMarkdownPreview(fileID, delimiter string, hasHeader bool, maxRows int) (CsvTextPreviewResult, error) {
	parsedDelimiter, err := parseCSVDelimiter(delimiter)
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	maxRows = clampRequestInt(maxRows, defaultCSVPreviewRows, maxCSVPreviewRows)
	sample, err := s.csvSample(fileID)
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	data, _, err := prepareCSVSample(context.Background(), sample, parsedDelimiter)
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	rep, err := csv.PreviewRowsContext(context.Background(), bytes.NewReader(data), csv.PreviewOptions{
		Delimiter: parsedDelimiter,
		HasHeader: hasHeader,
		MaxBytes:  decodedSampleLimit(data),
		MaxRows:   maxRows,
	})
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	return CsvTextPreviewResult{Generation: sample.generation, Text: csv.MarkdownPreview(rep.Header, rep.Rows)}, nil
}

// CsvToSQLConfigPreview returns a short sample of the SQL for a full config.
func (s *FileService) CsvToSQLConfigPreview(fileID string, cfg CsvSqlConfig) (CsvTextPreviewResult, error) {
	opts, err := csvSqlOptions(cfg)
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	sample, err := s.csvSample(fileID)
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	data, _, err := prepareCSVSample(context.Background(), sample, opts.Delimiter)
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	rep, err := csv.PreviewSQLConversionContext(context.Background(), bytes.NewReader(data), csv.SQLPreviewOptions{
		SQLConvertOptions: opts,
		MaxBytes:          decodedSampleLimit(data),
		MaxRows:           20,
	})
	if err != nil {
		return CsvTextPreviewResult{}, err
	}
	return CsvTextPreviewResult{Generation: sample.generation, Text: rep.SQL}, nil
}

// CsvToSQLConfigViaDialog streams the whole CSV to a .sql file using a full
// column-mapping config (select/rename/type, insert mode, batch, null, policy).
func (s *FileService) CsvToSQLConfigViaDialog(fileID string, sourceGeneration uint64, cfg CsvSqlConfig) (TransformResult, error) {
	opts, err := csvSqlOptions(cfg)
	if err != nil {
		return TransformResult{}, err
	}
	if err := s.csvTransformPreflight(fileID, sourceGeneration); err != nil {
		return TransformResult{}, err
	}
	dst, err := csvTransformSaveDialog("Save SQL as", opts.TableName+".sql")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withCSVTransformJob(fileID, sourceGeneration, "Convert CSV to SQL", func(ctx context.Context, file *session.File, progress func(int64, string)) (TransformResult, error) {
		jobOpts := opts
		jobOpts.Progress = func(p csv.SQLConvertProgress) { progress(p.RecordsRead, "rows read") }
		sum, err := convertCSVToSQLVerified(ctx, file.Doc, file.Path, dst, jobOpts, func(completed int64, phase string) {
			progress(completed, phase)
		})
		if err != nil {
			return TransformResult{}, err
		}
		note := fmt.Sprintf("table %q · %d rows", sum.TableName, sum.RowsWritten)
		if sum.SkippedRows > 0 || sum.SanitizedRows > 0 {
			note += fmt.Sprintf(" (%d skipped, %d sanitized)", sum.SkippedRows, sum.SanitizedRows)
		}
		return TransformResult{OutputPath: dst, RecordsRead: sum.RecordsRead, RecordsWritten: sum.RowsWritten, Note: note}, nil
	})
}
