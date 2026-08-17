package csv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	DefaultPreviewMaxBytes = int64(4 * 1024 * 1024)
	DefaultPreviewMaxRows  = 20
)

const (
	maxPreviewCellRunes = 64
	maxPreviewColumns   = 40
)

type PreviewOptions struct {
	Delimiter      rune
	HasHeader      bool
	MaxBytes       int64
	MaxRows        int
	MaxRecordBytes int64
}

type PreviewReport struct {
	Header          []string
	Rows            [][]string
	RecordsScanned  int
	BytesScanned    int64
	TruncatedSample bool
	Warnings        []string
}

func PreviewRows(r io.Reader, opts PreviewOptions) (PreviewReport, error) {
	return PreviewRowsContext(context.Background(), r, opts)
}

func PreviewRowsContext(ctx context.Context, r io.Reader, opts PreviewOptions) (PreviewReport, error) {
	if r == nil {
		return PreviewReport{}, errors.New("reader is required")
	}
	opts, err := normalizePreviewOptions(opts)
	if err != nil {
		return PreviewReport{}, err
	}

	sample, err := readBoundedSampleResult(ctx, r, opts.MaxBytes)
	if err != nil {
		return PreviewReport{}, err
	}
	data := sample.Data
	partialOmitted := false
	if sample.Truncated {
		data, partialOmitted, err = CompleteRecordPrefix(ctx, data, opts.Delimiter, opts.MaxRecordBytes, true)
		if err != nil {
			return PreviewReport{}, err
		}
	}

	report := PreviewReport{
		BytesScanned:    sample.BytesScanned,
		TruncatedSample: sample.Truncated,
	}
	if len(bytes.TrimSpace(data)) == 0 {
		report.Warnings = append(report.Warnings, "sample is empty")
		return report, nil
	}
	if sample.Truncated {
		report.Warnings = append(report.Warnings, fmt.Sprintf("sample limited to %d bytes", opts.MaxBytes))
	}
	if partialOmitted {
		report.Warnings = append(report.Warnings, "trailing partial logical record omitted from preview")
	}

	reader, err := newBoundedCSVReader(ctx, bytes.NewReader(data), csvReaderConfig{
		Delimiter: opts.Delimiter, MaxRecordBytes: opts.MaxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return report, err
	}

	if opts.HasHeader {
		header, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return report, nil
		}
		if err != nil {
			return report, err
		}
		report.Header = append([]string(nil), header...)
		report.RecordsScanned++
	}

	for len(report.Rows) < opts.MaxRows {
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
		report.Rows = append(report.Rows, append([]string(nil), record...))
		report.RecordsScanned++
	}
	return report, nil
}

func FormatPreviewReport(report PreviewReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Rows shown: %d\n", len(report.Rows))
	fmt.Fprintf(&b, "Records scanned: %d\n", report.RecordsScanned)
	fmt.Fprintf(&b, "Bytes scanned: %d\n", report.BytesScanned)
	fmt.Fprintf(&b, "Truncated sample: %t\n", report.TruncatedSample)
	if len(report.Header) > 0 {
		fmt.Fprintf(&b, "Header: %s\n", strings.Join(formatPreviewRecord(report.Header), " | "))
	}
	if len(report.Rows) == 0 {
		if len(report.Warnings) > 0 {
			fmt.Fprintf(&b, "\nWarnings:\n%s\n", strings.Join(report.Warnings, "\n"))
		}
		return strings.TrimRight(b.String(), "\n")
	}

	b.WriteString("\nRows:\n")
	for i, row := range report.Rows {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.Join(formatPreviewRecord(row), " | "))
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "\nWarnings:\n%s\n", strings.Join(report.Warnings, "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func normalizePreviewOptions(opts PreviewOptions) (PreviewOptions, error) {
	maxBytes, err := normalizeSampleByteLimit("preview sample", opts.MaxBytes, DefaultPreviewMaxBytes)
	if err != nil {
		return opts, err
	}
	maxRows, err := normalizeSampleRowLimit("preview sample", opts.MaxRows, DefaultPreviewMaxRows)
	if err != nil {
		return opts, err
	}
	opts.MaxBytes = maxBytes
	opts.MaxRows = maxRows
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if !validProjectDelimiter(opts.Delimiter) {
		return opts, fmt.Errorf("invalid delimiter %q", opts.Delimiter)
	}
	limit, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes)
	if err != nil {
		return opts, err
	}
	opts.MaxRecordBytes = limit
	return opts, nil
}

func formatPreviewRecord(record []string) []string {
	display := record
	omitted := 0
	if len(display) > maxPreviewColumns {
		omitted = len(display) - maxPreviewColumns
		display = display[:maxPreviewColumns]
	}
	out := make([]string, 0, len(display)+1)
	for _, value := range display {
		out = append(out, trimPreviewCell(value))
	}
	if omitted > 0 {
		out = append(out, "... "+strconv.Itoa(omitted)+" more columns")
	}
	return out
}

func trimPreviewCell(value string) string {
	runes := []rune(value)
	if len(runes) > maxPreviewCellRunes {
		value = string(runes[:maxPreviewCellRunes]) + "..."
	}
	return strconv.Quote(value)
}
