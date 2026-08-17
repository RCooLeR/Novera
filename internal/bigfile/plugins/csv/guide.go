package csv

import (
	"context"
	stdcsv "encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const DefaultColumnGuideMaxColumns = 500

type ColumnGuideOptions struct {
	Delimiter      rune
	HasHeader      bool
	MaxBytes       int64
	MaxRows        int
	MaxRecordBytes int64
	MaxColumns     int
	NullValues     []string
}

type ColumnGuideReport struct {
	Schema           SchemaReport
	ProjectionText   string
	SQLColumns       []string
	SQLColumnsText   string
	ColumnsIncluded  int
	TruncatedColumns bool
	Warnings         []string
}

func BuildColumnGuide(r io.Reader, opts ColumnGuideOptions) (ColumnGuideReport, error) {
	return BuildColumnGuideContext(context.Background(), r, opts)
}

func BuildColumnGuideContext(ctx context.Context, r io.Reader, opts ColumnGuideOptions) (ColumnGuideReport, error) {
	opts, schemaOpts, err := normalizeColumnGuideOptions(opts)
	if err != nil {
		return ColumnGuideReport{}, err
	}
	schema, err := InferSchemaContext(ctx, r, schemaOpts)
	if err != nil {
		return ColumnGuideReport{}, err
	}

	included := len(schema.Columns)
	report := ColumnGuideReport{Schema: schema}
	if included > opts.MaxColumns {
		included = opts.MaxColumns
		report.TruncatedColumns = true
		report.Warnings = append(report.Warnings, fmt.Sprintf("column guide limited to first %d columns", opts.MaxColumns))
	}
	report.ColumnsIncluded = included
	if included == 0 {
		return report, nil
	}

	projection := make([]string, included)
	report.SQLColumns = make([]string, included)
	for i := 0; i < included; i++ {
		if err := contextErr(ctx); err != nil {
			return report, err
		}
		projection[i] = strconv.Itoa(i + 1)
		report.SQLColumns[i] = schema.Columns[i].Name
	}
	report.ProjectionText = strings.Join(projection, ",")
	report.SQLColumnsText = csvRecordLine(report.SQLColumns)
	return report, nil
}

func FormatColumnGuide(report ColumnGuideReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Columns discovered: %d\n", len(report.Schema.Columns))
	fmt.Fprintf(&b, "Columns included in guide: %d\n", report.ColumnsIncluded)
	fmt.Fprintf(&b, "Records scanned: %d\n", report.Schema.RecordsScanned)
	fmt.Fprintf(&b, "Bytes scanned: %d\n", report.Schema.BytesScanned)
	fmt.Fprintf(&b, "Header row: %t\n", report.Schema.HasHeader)
	fmt.Fprintf(&b, "Truncated sample: %t\n", report.Schema.TruncatedSample)
	if report.ProjectionText != "" {
		fmt.Fprintf(&b, "\nProject columns value:\n%s\n", report.ProjectionText)
	}
	if report.SQLColumnsText != "" {
		fmt.Fprintf(&b, "\nSQL explicit columns value:\n%s\n", report.SQLColumnsText)
	}
	if report.ColumnsIncluded > 0 {
		b.WriteString("\nColumn map:\n")
		displayColumns := report.Schema.Columns
		if len(displayColumns) > report.ColumnsIncluded {
			displayColumns = displayColumns[:report.ColumnsIncluded]
		}
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
		if omitted := len(report.Schema.Columns) - len(displayColumns); omitted > 0 {
			fmt.Fprintf(&b, "... %d more columns omitted from guide preview\n", omitted)
		}
	}

	warnings := append([]string{}, report.Schema.Warnings...)
	warnings = append(warnings, report.Warnings...)
	if len(warnings) > 0 {
		fmt.Fprintf(&b, "\nWarnings:\n%s\n", strings.Join(warnings, "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func normalizeColumnGuideOptions(opts ColumnGuideOptions) (ColumnGuideOptions, SchemaOptions, error) {
	if opts.MaxColumns <= 0 {
		opts.MaxColumns = DefaultColumnGuideMaxColumns
	}
	schemaOpts, err := normalizeSchemaOptions(SchemaOptions{
		Delimiter:      opts.Delimiter,
		HasHeader:      opts.HasHeader,
		MaxBytes:       opts.MaxBytes,
		MaxRows:        opts.MaxRows,
		MaxRecordBytes: opts.MaxRecordBytes,
		NullValues:     opts.NullValues,
	})
	if err != nil {
		return opts, schemaOpts, err
	}
	opts.Delimiter = schemaOpts.Delimiter
	opts.MaxBytes = schemaOpts.MaxBytes
	opts.MaxRows = schemaOpts.MaxRows
	opts.MaxRecordBytes = schemaOpts.MaxRecordBytes
	return opts, schemaOpts, nil
}

func csvRecordLine(fields []string) string {
	var b strings.Builder
	writer := stdcsv.NewWriter(&b)
	_ = writer.Write(fields)
	writer.Flush()
	return strings.TrimRight(b.String(), "\r\n")
}
