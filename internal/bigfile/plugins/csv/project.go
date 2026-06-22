package csv

import (
	"bufio"
	"bytes"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"novera/internal/bigfile/fileio"
)

type ProjectOptions struct {
	Delimiter         rune
	Columns           []int
	AllowShortRecords bool
	MissingValue      string
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
	reader := stdcsv.NewReader(counting)
	reader.Comma = opts.Delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	reader.ReuseRecord = false

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

func ProjectColumnsFile(ctx context.Context, inputPath, outputPath string, opts ProjectOptions) (ProjectSummary, error) {
	if inputPath == "" {
		return ProjectSummary{}, errors.New("input path is required")
	}
	if outputPath == "" {
		return ProjectSummary{}, errors.New("output path is required")
	}
	same, err := sameFilePath(inputPath, outputPath)
	if err != nil {
		return ProjectSummary{}, err
	}
	if same {
		return ProjectSummary{}, errors.New("output path must be different from input path")
	}

	input, err := os.Open(inputPath)
	if err != nil {
		return ProjectSummary{}, err
	}
	defer input.Close()

	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return ProjectSummary{}, err
	}

	cleanup := true
	defer func() {
		if cleanup {
			_ = output.Close()
			_ = os.Remove(outputPath)
		}
	}()

	summary, err := ProjectColumns(ctx, input, output, opts)
	if err != nil {
		return summary, err
	}
	if err := output.Sync(); err != nil {
		return summary, err
	}
	if err := output.Close(); err != nil {
		return summary, err
	}
	cleanup = false
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

	data, err := readBoundedSample(ctx, r, maxBytes)
	if err != nil {
		return ProjectPreviewReport{}, err
	}
	truncated := int64(len(data)) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	bytesScanned := int64(len(data))
	if truncated {
		if trimmed, ok := trimTrailingPartialRecord(data); ok {
			data = trimmed
		}
	}

	report := ProjectPreviewReport{
		BytesScanned:    bytesScanned,
		ColumnsWritten:  len(projectOpts.Columns),
		Delimiter:       projectOpts.Delimiter,
		TruncatedSample: truncated,
	}
	if len(bytes.TrimSpace(data)) == 0 {
		report.Warnings = append(report.Warnings, "sample is empty")
		return report, nil
	}
	if truncated {
		report.Warnings = append(report.Warnings, fmt.Sprintf("sample limited to %d bytes", maxBytes))
		report.Warnings = append(report.Warnings, "trailing partial record omitted from preview")
	}

	reader := stdcsv.NewReader(bytes.NewReader(data))
	reader.Comma = projectOpts.Delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = false

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
		report.Rows = append(report.Rows, projected)
	}
	return report, nil
}

func trimTrailingPartialRecord(data []byte) ([]byte, bool) {
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			return data[:i+1], true
		}
		if data[i] == '\r' {
			return data[:i+1], true
		}
	}
	return nil, false
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
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if !validProjectDelimiter(opts.Delimiter) {
		return opts, fmt.Errorf("invalid delimiter %q", opts.Delimiter)
	}
	if len(opts.Columns) == 0 {
		return opts, errors.New("at least one output column is required")
	}
	for i, column := range opts.Columns {
		if column < 0 {
			return opts, fmt.Errorf("column %d is negative", i)
		}
	}
	return opts, nil
}

func normalizeProjectPreviewOptions(opts ProjectPreviewOptions) (ProjectOptions, int64, int, error) {
	projectOpts, err := normalizeProjectOptions(ProjectOptions{
		Delimiter:         opts.Delimiter,
		Columns:           opts.Columns,
		AllowShortRecords: opts.AllowShortRecords,
		MissingValue:      opts.MissingValue,
	})
	if err != nil {
		return projectOpts, 0, 0, err
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultPreviewMaxBytes
	}
	maxRows := opts.MaxRows
	if maxRows <= 0 {
		maxRows = DefaultPreviewMaxRows
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
	return delimiter != '"' && delimiter != '\r' && delimiter != '\n' && utf8.ValidRune(delimiter) && delimiter != utf8.RuneError
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
