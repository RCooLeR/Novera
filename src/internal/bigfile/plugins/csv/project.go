package csv

import (
	"bufio"
	"bytes"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/regularfile"
)

type ProjectOptions struct {
	Delimiter         rune
	Columns           []int
	AllowShortRecords bool
	MissingValue      string
	MaxRecordBytes    int64
	Progress          func(ProjectProgress)
}

type ProjectProgress struct {
	RecordsRead    int64
	RecordsWritten int64
	BytesRead      int64
}

type ProjectSummary struct {
	RecordsRead    int64
	RecordsWritten int64
	BytesRead      int64
	ColumnsWritten int
	Delimiter      rune
}

type ProjectPreviewOptions struct {
	Delimiter         rune
	Columns           []int
	AllowShortRecords bool
	MissingValue      string
	MaxBytes          int64
	MaxRows           int
	MaxRecordBytes    int64
}

type ProjectPreviewReport struct {
	Rows            [][]string
	RecordsRead     int
	BytesScanned    int64
	ColumnsWritten  int
	Delimiter       rune
	TruncatedSample bool
	Warnings        []string
}

func ProjectColumns(ctx context.Context, r io.Reader, w io.Writer, opts ProjectOptions) (ProjectSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return ProjectSummary{}, errors.New("reader is required")
	}
	if w == nil {
		return ProjectSummary{}, errors.New("writer is required")
	}
	opts, err := normalizeProjectOptions(opts)
	if err != nil {
		return ProjectSummary{}, err
	}

	br := bufio.NewReader(r)
	if err := skipInputBOM(br); err != nil {
		return ProjectSummary{}, err
	}
	counting := &countingReader{r: br}
	reader, err := newBoundedCSVReader(ctx, counting, csvReaderConfig{
		Delimiter: opts.Delimiter, MaxRecordBytes: opts.MaxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return ProjectSummary{}, err
	}

	writer := stdcsv.NewWriter(w)
	writer.Comma = opts.Delimiter

	summary := ProjectSummary{
		ColumnsWritten: len(opts.Columns),
		Delimiter:      opts.Delimiter,
	}
	for {
		if err := ctx.Err(); err != nil {
			summary.BytesRead = counting.n
			return summary, err
		}

		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			summary.BytesRead = counting.n
			return summary, err
		}
		summary.RecordsRead++

		projected, err := projectRecord(record, opts)
		if err != nil {
			summary.BytesRead = counting.n
			return summary, fmt.Errorf("record %d: %w", summary.RecordsRead, err)
		}
		if err := validateCSVOutputRecordSize("projected CSV record", projected, opts.Delimiter); err != nil {
			summary.BytesRead = counting.n
			return summary, fmt.Errorf("record %d: %w", summary.RecordsRead, err)
		}
		if err := writer.Write(projected); err != nil {
			summary.BytesRead = counting.n
			return summary, err
		}
		summary.RecordsWritten++
		summary.BytesRead = counting.n
		if opts.Progress != nil {
			opts.Progress(ProjectProgress{
				RecordsRead:    summary.RecordsRead,
				RecordsWritten: summary.RecordsWritten,
				BytesRead:      summary.BytesRead,
			})
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		summary.BytesRead = counting.n
		return summary, err
	}
	summary.BytesRead = counting.n
	return summary, nil
}

func ProjectColumnsFile(ctx context.Context, inputPath, outputPath string, opts ProjectOptions) (_ ProjectSummary, retErr error) {
	if inputPath == "" {
		return ProjectSummary{}, errors.New("input path is required")
	}
	if outputPath == "" {
		return ProjectSummary{}, errors.New("output path is required")
	}
	normalized, err := normalizeProjectOptions(opts)
	if err != nil {
		return ProjectSummary{}, err
	}
	opts = normalized

	input, err := regularfile.Open(inputPath)
	if err != nil {
		return ProjectSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, input.Close()) }()

	output, err := fileio.OpenAtomicOutput(outputPath, []string{inputPath}, 0o600)
	if err != nil {
		return ProjectSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, output.Cleanup()) }()

	summary, err := ProjectColumns(ctx, input, output, opts)
	if err != nil {
		return summary, err
	}
	if err := output.CommitContext(ctx); err != nil {
		return summary, err
	}
	return summary, nil
}

func PreviewProjectedColumns(r io.Reader, opts ProjectPreviewOptions) (ProjectPreviewReport, error) {
	return PreviewProjectedColumnsContext(context.Background(), r, opts)
}

func PreviewProjectedColumnsContext(ctx context.Context, r io.Reader, opts ProjectPreviewOptions) (ProjectPreviewReport, error) {
	if r == nil {
		return ProjectPreviewReport{}, errors.New("reader is required")
	}
	projectOpts, maxBytes, maxRows, err := normalizeProjectPreviewOptions(opts)
	if err != nil {
		return ProjectPreviewReport{}, err
	}

	sample, err := readBoundedSampleResult(ctx, r, maxBytes)
	if err != nil {
		return ProjectPreviewReport{}, err
	}
	data := sample.Data
	partialOmitted := false
	if sample.Truncated {
		data, partialOmitted, err = CompleteRecordPrefix(ctx, data, projectOpts.Delimiter, projectOpts.MaxRecordBytes, true)
		if err != nil {
			return ProjectPreviewReport{}, err
		}
	}

	report := ProjectPreviewReport{
		BytesScanned:    sample.BytesScanned,
		ColumnsWritten:  len(projectOpts.Columns),
		Delimiter:       projectOpts.Delimiter,
		TruncatedSample: sample.Truncated,
	}
	if len(bytes.TrimSpace(data)) == 0 {
		report.Warnings = append(report.Warnings, "sample is empty")
		return report, nil
	}
	if sample.Truncated {
		report.Warnings = append(report.Warnings, fmt.Sprintf("sample limited to %d bytes", maxBytes))
	}
	if partialOmitted {
		report.Warnings = append(report.Warnings, "trailing partial record omitted from preview")
	}

	reader, err := newBoundedCSVReader(ctx, bytes.NewReader(data), csvReaderConfig{
		Delimiter: projectOpts.Delimiter, MaxRecordBytes: projectOpts.MaxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return report, err
	}

	for len(report.Rows) < maxRows {
		if err := contextErr(ctx); err != nil {
			return report, err
		}
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return report, err
		}
		report.RecordsRead++
		projected, err := projectRecord(record, projectOpts)
		if err != nil {
			return report, fmt.Errorf("record %d: %w", report.RecordsRead, err)
		}
		if err := validateCSVOutputRecordSize("projected CSV preview record", projected, projectOpts.Delimiter); err != nil {
			return report, fmt.Errorf("record %d: %w", report.RecordsRead, err)
		}
		report.Rows = append(report.Rows, projected)
	}
	return report, nil
}

func FormatProjectPreviewReport(report ProjectPreviewReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Projected rows shown: %d\n", len(report.Rows))
	fmt.Fprintf(&b, "Records read: %d\n", report.RecordsRead)
	fmt.Fprintf(&b, "Bytes scanned: %d\n", report.BytesScanned)
	fmt.Fprintf(&b, "Columns written: %d\n", report.ColumnsWritten)
	fmt.Fprintf(&b, "Delimiter: %q\n", report.Delimiter)
	fmt.Fprintf(&b, "Truncated sample: %t\n", report.TruncatedSample)
	if len(report.Rows) > 0 {
		b.WriteString("\nProjected output sample:\n")
		for _, row := range report.Rows {
			b.WriteString(csvRecordLineWithDelimiter(row, report.Delimiter))
			b.WriteByte('\n')
		}
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "\nWarnings:\n%s\n", strings.Join(report.Warnings, "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func normalizeProjectOptions(opts ProjectOptions) (ProjectOptions, error) {
	if err := validateTransformColumnMappingCount("CSV projection", len(opts.Columns), true); err != nil {
		return opts, err
	}
	if err := validateDistinctNonNegativeIndexes("projection column", opts.Columns); err != nil {
		return opts, err
	}
	configStringBytes := 0
	if err := addTransformConfigString(&configStringBytes, "CSV projection", opts.MissingValue); err != nil {
		return opts, err
	}
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if !validProjectDelimiter(opts.Delimiter) {
		return opts, fmt.Errorf("invalid delimiter %q", opts.Delimiter)
	}
	if opts.AllowShortRecords {
		if err := validateRepeatedCSVOutputField(
			"CSV projection missing-value expansion",
			opts.MissingValue,
			len(opts.Columns),
			opts.Delimiter,
		); err != nil {
			return opts, err
		}
	}
	limit, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes)
	if err != nil {
		return opts, err
	}
	opts.MaxRecordBytes = limit
	return opts, nil
}

// ValidateProjectOptions validates all caller-controlled projection settings
// without opening a source or creating an output.
func ValidateProjectOptions(opts ProjectOptions) error {
	_, err := normalizeProjectOptions(opts)
	return err
}

func normalizeProjectPreviewOptions(opts ProjectPreviewOptions) (ProjectOptions, int64, int, error) {
	maxBytes, err := normalizeSampleByteLimit("project preview sample", opts.MaxBytes, DefaultPreviewMaxBytes)
	if err != nil {
		return ProjectOptions{}, 0, 0, err
	}
	maxRows, err := normalizeSampleRowLimit("project preview sample", opts.MaxRows, DefaultPreviewMaxRows)
	if err != nil {
		return ProjectOptions{}, 0, 0, err
	}
	projectOpts, err := normalizeProjectOptions(ProjectOptions{
		Delimiter:         opts.Delimiter,
		Columns:           opts.Columns,
		AllowShortRecords: opts.AllowShortRecords,
		MissingValue:      opts.MissingValue,
		MaxRecordBytes:    opts.MaxRecordBytes,
	})
	if err != nil {
		return projectOpts, 0, 0, err
	}
	return projectOpts, maxBytes, maxRows, nil
}

func projectRecord(record []string, opts ProjectOptions) ([]string, error) {
	projected := make([]string, len(opts.Columns))
	for i, column := range opts.Columns {
		if column >= len(record) {
			if !opts.AllowShortRecords {
				return nil, fmt.Errorf("column %d is missing from %d-field record", column, len(record))
			}
			projected[i] = opts.MissingValue
			continue
		}
		projected[i] = record[column]
	}
	return projected, nil
}

func validProjectDelimiter(delimiter rune) bool {
	return ValidateDelimiter(delimiter) == nil
}

// ValidateDelimiter rejects delimiters that encoding/csv cannot represent.
func ValidateDelimiter(delimiter rune) error {
	if delimiter == 0 || delimiter == '"' || delimiter == '\r' || delimiter == '\n' || !utf8.ValidRune(delimiter) || delimiter == utf8.RuneError {
		return fmt.Errorf("invalid delimiter %q", delimiter)
	}
	return nil
}

func sameFilePath(a, b string) (bool, error) {
	return fileio.SamePath(a, b)
}

func csvRecordLineWithDelimiter(fields []string, delimiter rune) string {
	var b strings.Builder
	writer := stdcsv.NewWriter(&b)
	writer.Comma = delimiter
	_ = writer.Write(fields)
	writer.Flush()
	return strings.TrimRight(b.String(), "\r\n")
}

type countingReader struct {
	r io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}
