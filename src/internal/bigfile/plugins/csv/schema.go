package csv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

const (
	DefaultSchemaMaxBytes = int64(4 * 1024 * 1024)
	DefaultSchemaMaxRows  = 200
)

const (
	SQLTypeBigInt  = "BIGINT"
	SQLTypeDouble  = "DOUBLE"
	SQLTypeBoolean = "BOOLEAN"
	SQLTypeText    = "TEXT"
)

const (
	maxSchemaSampleValueRunes = 48
	maxSchemaPreviewColumns   = 80
)

type SchemaOptions struct {
	Delimiter      rune
	HasHeader      bool
	MaxBytes       int64
	MaxRows        int
	MaxRecordBytes int64
	NullValues     []string
}

type SchemaColumn struct {
	Name         string
	SQLType      string
	NonNullCount int
	NullCount    int
	SampleValues []string
}

type SchemaReport struct {
	Columns         []SchemaColumn
	RecordsScanned  int
	BytesScanned    int64
	TruncatedSample bool
	HasHeader       bool
	Warnings        []string
}

func InferSchema(r io.Reader, opts SchemaOptions) (SchemaReport, error) {
	return InferSchemaContext(context.Background(), r, opts)
}

func InferSchemaContext(ctx context.Context, r io.Reader, opts SchemaOptions) (SchemaReport, error) {
	if r == nil {
		return SchemaReport{}, errors.New("reader is required")
	}
	opts, err := normalizeSchemaOptions(opts)
	if err != nil {
		return SchemaReport{}, err
	}

	sample, err := readBoundedSampleResult(ctx, r, opts.MaxBytes)
	if err != nil {
		return SchemaReport{}, err
	}
	data := sample.Data
	partialOmitted := false
	if sample.Truncated {
		data, partialOmitted, err = CompleteRecordPrefix(ctx, data, opts.Delimiter, opts.MaxRecordBytes, true)
		if err != nil {
			return SchemaReport{}, err
		}
	}

	report := SchemaReport{
		BytesScanned:    sample.BytesScanned,
		TruncatedSample: sample.Truncated,
		HasHeader:       opts.HasHeader,
	}
	if len(bytes.TrimSpace(data)) == 0 {
		report.Warnings = append(report.Warnings, "sample is empty")
		return report, nil
	}
	if sample.Truncated {
		report.Warnings = append(report.Warnings, fmt.Sprintf("sample limited to %d bytes", opts.MaxBytes))
	}
	if partialOmitted {
		report.Warnings = append(report.Warnings, "trailing partial logical record omitted from schema inference")
	}

	reader, err := newBoundedCSVReader(ctx, bytes.NewReader(data), csvReaderConfig{
		Delimiter: opts.Delimiter, MaxRecordBytes: opts.MaxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return report, err
	}

	var columns []SchemaColumn
	kinds := []schemaKind{}
	nulls := makeSQLNullSet(opts.NullValues)
	for report.RecordsScanned < opts.MaxRows {
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
		report.RecordsScanned++
		if report.RecordsScanned == 1 {
			if opts.HasHeader {
				names := normalizeSQLColumnNames(record)
				columns = make([]SchemaColumn, len(names))
				kinds = make([]schemaKind, len(names))
				for i, name := range names {
					columns[i].Name = name
				}
				continue
			}
			columns = defaultSchemaColumns(len(record))
			kinds = make([]schemaKind, len(record))
		}
		if len(record) > len(columns) {
			start := len(columns)
			columns = append(columns, defaultSchemaColumns(len(record)-len(columns))...)
			for i := start; i < len(columns); i++ {
				columns[i].Name = fmt.Sprintf("column_%d", i+1)
			}
			kinds = append(kinds, make([]schemaKind, len(record)-len(kinds))...)
			report.Warnings = append(report.Warnings, fmt.Sprintf("record %d has more fields than earlier rows", report.RecordsScanned))
		}
		if len(record) < len(columns) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("record %d has fewer fields than expected", report.RecordsScanned))
		}
		for i := range columns {
			value := ""
			if i < len(record) {
				value = record[i]
			}
			if _, ok := nulls[value]; ok {
				columns[i].NullCount++
				continue
			}
			columns[i].NonNullCount++
			if len(columns[i].SampleValues) < 3 {
				columns[i].SampleValues = append(columns[i].SampleValues, value)
			}
			kinds[i] = promoteSchemaKind(kinds[i], classifySchemaValue(value))
		}
	}
	for i := range columns {
		columns[i].SQLType = schemaKindSQLType(kinds[i])
	}
	report.Columns = columns
	return report, nil
}

func FormatSchemaReport(report SchemaReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Columns: %d\n", len(report.Columns))
	fmt.Fprintf(&b, "Records scanned: %d\n", report.RecordsScanned)
	fmt.Fprintf(&b, "Bytes scanned: %d\n", report.BytesScanned)
	fmt.Fprintf(&b, "Header row: %t\n", report.HasHeader)
	fmt.Fprintf(&b, "Truncated sample: %t\n", report.TruncatedSample)
	if len(report.Columns) == 0 {
		if len(report.Warnings) > 0 {
			fmt.Fprintf(&b, "\nWarnings:\n%s\n", strings.Join(report.Warnings, "\n"))
		}
		return strings.TrimRight(b.String(), "\n")
	}

	b.WriteString("\nInferred columns:\n")
	displayColumns := report.Columns
	if len(displayColumns) > maxSchemaPreviewColumns {
		displayColumns = displayColumns[:maxSchemaPreviewColumns]
	}
	for i, column := range displayColumns {
		fmt.Fprintf(&b, "%d. %s | %s | non-null: %d | null: %d", i+1, column.Name, column.SQLType, column.NonNullCount, column.NullCount)
		if len(column.SampleValues) > 0 {
			fmt.Fprintf(&b, " | samples: %s", strings.Join(trimSchemaSamples(column.SampleValues), ", "))
		}
		b.WriteByte('\n')
	}
	if omitted := len(report.Columns) - len(displayColumns); omitted > 0 {
		fmt.Fprintf(&b, "... %d more columns omitted from preview\n", omitted)
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "\nWarnings:\n%s\n", strings.Join(report.Warnings, "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func normalizeSchemaOptions(opts SchemaOptions) (SchemaOptions, error) {
	maxBytes, err := normalizeSampleByteLimit("schema sample", opts.MaxBytes, DefaultSchemaMaxBytes)
	if err != nil {
		return opts, err
	}
	maxRows, err := normalizeSampleRowLimit("schema sample", opts.MaxRows, DefaultSchemaMaxRows)
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

func trimSchemaSamples(values []string) []string {
	trimmed := make([]string, len(values))
	for i, value := range values {
		runes := []rune(value)
		if len(runes) > maxSchemaSampleValueRunes {
			value = string(runes[:maxSchemaSampleValueRunes]) + "..."
		}
		trimmed[i] = strconv.Quote(value)
	}
	return trimmed
}

func defaultSchemaColumns(count int) []SchemaColumn {
	columns := make([]SchemaColumn, count)
	for i := range columns {
		columns[i].Name = fmt.Sprintf("column_%d", i+1)
	}
	return columns
}

type schemaKind int

const (
	schemaUnknown schemaKind = iota
	schemaInt
	schemaFloat
	schemaBool
	schemaText
)

func classifySchemaValue(value string) schemaKind {
	if value == "" {
		return schemaText
	}
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" {
		// The MySQL writer validates BOOLEAN columns during the full conversion
		// and emits these tokens as canonical unquoted 1/0 literals.
		return schemaBool
	}
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		// A value like 007 / 00123 parses as an int but is almost certainly an
		// identifier (zip/account/leading-zero id). Since values are stored as
		// quoted strings, a BIGINT column would silently drop the leading zeros on
		// import вЂ” so classify it as TEXT to preserve it.
		if looksLikeLeadingZeroInt(value) {
			return schemaText
		}
		return schemaInt
	}
	if parsed, err := strconv.ParseFloat(value, 64); err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
		return schemaFloat
	}
	return schemaText
}

// looksLikeLeadingZeroInt reports whether s is a multi-digit integer with a
// leading zero (e.g. 007, -0042), which should be preserved as TEXT.
func looksLikeLeadingZeroInt(s string) bool {
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	return len(s) > 1 && s[0] == '0'
}

func promoteSchemaKind(current, next schemaKind) schemaKind {
	if current == schemaUnknown {
		return next
	}
	if next == schemaUnknown || current == next {
		return current
	}
	if (current == schemaInt && next == schemaFloat) || (current == schemaFloat && next == schemaInt) {
		return schemaFloat
	}
	return schemaText
}

func schemaKindSQLType(kind schemaKind) string {
	switch kind {
	case schemaInt:
		return SQLTypeBigInt
	case schemaFloat:
		return SQLTypeDouble
	case schemaBool:
		return SQLTypeBoolean
	default:
		return SQLTypeText
	}
}
