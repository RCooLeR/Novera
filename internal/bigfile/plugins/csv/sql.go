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

	"golang.org/x/text/cases"
	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/regularfile"
)

const DefaultSQLInsertBatchSize = 500
const MaxSQLInsertBatchSize = 10_000
const MaxSQLInsertBatchBytes int64 = 64 * 1024 * 1024
const DefaultSQLMaxFieldBytes = 16 * 1024 * 1024

const (
	sqlInsertTupleSeparator = ",\n  "
	sqlInsertTerminator     = ";\n"
)

var ErrSQLInsertBatchTooLarge = errors.New("encoded SQL INSERT batch exceeds byte limit")

type SQLDialect string

const SQLDialectMySQL SQLDialect = "mysql"

// SQLInsertMode is the complete allowlist of statement forms emitted by the
// CSV-to-SQL converter. Callers select a semantic mode; raw SQL prefixes are
// never accepted or interpolated.
type SQLInsertMode string

const (
	SQLInsertModeInsert       SQLInsertMode = "insert"
	SQLInsertModeInsertIgnore SQLInsertMode = "insert-ignore"
	SQLInsertModeReplace      SQLInsertMode = "replace"

	// MaxSQLIdentifierRunes is MySQL's character limit for table and column
	// identifiers. MaxSQLIdentifierBytes bounds inputs before a Unicode scan.
	MaxSQLIdentifierRunes = 64
	MaxSQLIdentifierBytes = 4 * MaxSQLIdentifierRunes
)

// InvalidValuePolicy controls what happens when a field contains a byte that
// can't be emitted safely (a control char or invalid UTF-8).
type InvalidValuePolicy string

const (
	// InvalidValueFail aborts the conversion (default — safest for correctness).
	InvalidValueFail InvalidValuePolicy = "fail"
	// InvalidValueSkipRow drops the offending record and continues.
	InvalidValueSkipRow InvalidValuePolicy = "skip-row"
	// InvalidValueReplace substitutes U+FFFD for the offending byte and continues.
	InvalidValueReplace InvalidValuePolicy = "replace"
)

type SQLConvertOptions struct {
	Delimiter       rune
	TableName       string
	Dialect         SQLDialect
	Columns         []string
	HasHeader       bool
	NullValues      []string
	InsertBatchSize int
	// MaxBatchBytes bounds one complete buffered INSERT statement, including
	// identifiers, separators, and terminator. Zero uses the hard maximum.
	MaxBatchBytes      int64
	IncludeCreateTable bool
	ColumnTypes        []string
	MaxFieldBytes      int64
	MaxRecordBytes     int64
	// SourceColumns selects which input fields to emit, in output order (0-based).
	// Empty means all fields in their original order. When set, len(Columns) and
	// len(ColumnTypes) must match len(SourceColumns).
	SourceColumns []int
	// InsertMode selects INSERT (default), INSERT IGNORE, or REPLACE. The
	// converter constructs the exact SQL tokens internally.
	InsertMode SQLInsertMode
	// OnInvalidValue selects how to handle un-emittable field bytes. Empty means
	// InvalidValueFail.
	OnInvalidValue InvalidValuePolicy
	Progress       func(SQLConvertProgress)
}

type SQLConvertProgress struct {
	RecordsRead int64
	RowsWritten int64
	BytesRead   int64
}

type SQLConvertSummary struct {
	RecordsRead        int64
	RowsWritten        int64
	BytesRead          int64
	Columns            int
	TableName          string
	Dialect            SQLDialect
	Delimiter          rune
	CreateTableWritten bool
	// SkippedRows / SanitizedRows count records dropped or repaired under a
	// non-fail OnInvalidValue policy.
	SkippedRows   int64
	SanitizedRows int64
}

type SQLPreviewOptions struct {
	SQLConvertOptions
	MaxBytes int64
	MaxRows  int
}

type SQLPreviewReport struct {
	SQL             string
	Summary         SQLConvertSummary
	BytesScanned    int64
	RowsPreviewed   int
	TruncatedSample bool
	Warnings        []string
}

func ConvertToSQL(ctx context.Context, r io.Reader, w io.Writer, opts SQLConvertOptions) (SQLConvertSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return SQLConvertSummary{}, errors.New("reader is required")
	}
	if w == nil {
		return SQLConvertSummary{}, errors.New("writer is required")
	}
	opts, err := normalizeSQLConvertOptions(opts)
	if err != nil {
		return SQLConvertSummary{}, err
	}

	br := bufio.NewReader(r)
	if err := skipInputBOM(br); err != nil {
		return SQLConvertSummary{}, err
	}
	counting := &countingReader{r: br}
	reader, err := newBoundedCSVReader(ctx, counting, csvReaderConfig{
		Delimiter: opts.Delimiter, MaxRecordBytes: opts.MaxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return SQLConvertSummary{}, err
	}

	writer := bufio.NewWriter(w)
	// Direct stream callers may inspect their own partial buffer after an error.
	// File-producing entry points stage the stream and never publish it unless
	// the complete conversion and validation succeed.
	defer func() { _ = writer.Flush() }()
	summary := SQLConvertSummary{
		TableName: opts.TableName,
		Dialect:   opts.Dialect,
		Delimiter: opts.Delimiter,
	}

	columns := append([]string(nil), opts.Columns...)
	var header []string
	if opts.HasHeader {
		header, err = reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return summary, errors.New("header row is required")
			}
			return summary, err
		}
		summary.RecordsRead++
		summary.BytesRead = counting.n
		if err := validateSQLRecordFieldSizes(header, opts.MaxFieldBytes); err != nil {
			return summary, fmt.Errorf("header row: %w", err)
		}
		if len(columns) == 0 {
			if err := validateTransformColumnMappingCount("CSV-to-SQL inferred header", len(header), true); err != nil {
				return summary, err
			}
			columns = normalizeSQLColumnNames(header)
		}
	}
	if len(columns) == 0 {
		return summary, errors.New("columns are required when input has no header")
	}
	if err := validateSQLIdentifiers("column", columns); err != nil {
		summary.BytesRead = counting.n
		return summary, err
	}
	columnTypes, err := resolveSQLColumnTypes(opts.ColumnTypes, len(columns))
	if err != nil {
		summary.BytesRead = counting.n
		return summary, err
	}
	if len(opts.SourceColumns) > 0 && len(opts.SourceColumns) != len(columns) {
		summary.BytesRead = counting.n
		return summary, fmt.Errorf("source-column mapping has %d positions, expected %d", len(opts.SourceColumns), len(columns))
	}
	if opts.HasHeader {
		if err := validateSQLSourceColumnRange(opts.SourceColumns, len(header)); err != nil {
			summary.BytesRead = counting.n
			return summary, err
		}
	}
	var pendingRecord []string
	if !opts.HasHeader && len(opts.SourceColumns) > 0 {
		pendingRecord, err = reader.Read()
		if errors.Is(err, io.EOF) {
			return summary, errors.New("cannot validate source-column mapping because input has no data records")
		}
		if err != nil {
			summary.BytesRead = counting.n
			return summary, err
		}
		if err := validateSQLSourceColumnRange(opts.SourceColumns, len(pendingRecord)); err != nil {
			summary.BytesRead = counting.n
			return summary, err
		}
	}
	summary.Columns = len(columns)
	insertHeader, err := sqlInsertBatchHeader(opts.InsertMode, opts.TableName, columns)
	if err != nil {
		summary.BytesRead = counting.n
		return summary, err
	}
	batchFramingBytes := int64(len(insertHeader) + len(sqlInsertTerminator))
	if batchFramingBytes > opts.MaxBatchBytes {
		return summary, fmt.Errorf("SQL INSERT prefix and terminator require %d bytes, exceeding batch limit %d: %w", batchFramingBytes, opts.MaxBatchBytes, ErrSQLInsertBatchTooLarge)
	}
	if opts.IncludeCreateTable {
		if err := writeSQLCreateTable(writer, opts.TableName, columns, columnTypes); err != nil {
			summary.BytesRead = counting.n
			return summary, err
		}
		summary.CreateTableWritten = true
	}

	nulls := makeSQLNullSet(opts.NullValues)
	batch := make([]string, 0, opts.InsertBatchSize)
	batchBytes := batchFramingBytes
	flushBatch := func(reportProgress bool) error {
		if len(batch) == 0 {
			return nil
		}
		if err := writeSQLInsertBatch(writer, insertHeader, batch); err != nil {
			return err
		}
		summary.RowsWritten += int64(len(batch))
		batch = batch[:0]
		batchBytes = batchFramingBytes
		summary.BytesRead = counting.n
		if reportProgress && opts.Progress != nil {
			opts.Progress(SQLConvertProgress{
				RecordsRead: summary.RecordsRead,
				RowsWritten: summary.RowsWritten,
				BytesRead:   summary.BytesRead,
			})
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			summary.BytesRead = counting.n
			return summary, err
		}

		var record []string
		if pendingRecord != nil {
			record = pendingRecord
			pendingRecord = nil
		} else {
			record, err = reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				summary.BytesRead = counting.n
				return summary, err
			}
		}
		summary.RecordsRead++
		// Project to the selected source columns in output order. A ragged row
		// that omits a selected field fails instead of manufacturing an empty one.
		row := record
		if len(opts.SourceColumns) > 0 {
			if err := validateSQLSourceColumnRange(opts.SourceColumns, len(record)); err != nil {
				summary.BytesRead = counting.n
				return summary, fmt.Errorf("record %d: %w", summary.RecordsRead, err)
			}
			row = make([]string, len(opts.SourceColumns))
			for i, si := range opts.SourceColumns {
				row[i] = record[si]
			}
		}
		if len(row) != len(columns) {
			summary.BytesRead = counting.n
			return summary, fmt.Errorf("record %d has %d fields, expected %d", summary.RecordsRead, len(row), len(columns))
		}
		if err := validateSQLRecordFieldSizes(row, opts.MaxFieldBytes); err != nil {
			summary.BytesRead = counting.n
			return summary, fmt.Errorf("record %d: %w", summary.RecordsRead, err)
		}

		tuple, skip, sanitized, err := sqlValuesTuple(row, nulls, columnTypes, opts.OnInvalidValue)
		if err != nil {
			summary.BytesRead = counting.n
			return summary, fmt.Errorf("record %d: %w", summary.RecordsRead, err)
		}
		if skip {
			summary.SkippedRows++
			continue
		}
		if sanitized {
			summary.SanitizedRows++
		}
		tupleBytes := int64(len(tuple))
		if tupleBytes > opts.MaxBatchBytes-batchFramingBytes {
			summary.BytesRead = counting.n
			return summary, fmt.Errorf("record %d encodes to %d SQL bytes plus %d framing bytes, exceeding batch limit %d: %w", summary.RecordsRead, tupleBytes, batchFramingBytes, opts.MaxBatchBytes, ErrSQLInsertBatchTooLarge)
		}
		if len(batch) > 0 {
			separatorBytes := int64(len(sqlInsertTupleSeparator))
			remaining := opts.MaxBatchBytes - batchBytes
			if remaining < separatorBytes || tupleBytes > remaining-separatorBytes {
				if err := flushBatch(true); err != nil {
					summary.BytesRead = counting.n
					return summary, err
				}
			}
		}
		if len(batch) > 0 {
			batchBytes += int64(len(sqlInsertTupleSeparator))
		}
		batch = append(batch, tuple)
		batchBytes += tupleBytes
		if len(batch) >= opts.InsertBatchSize {
			if err := flushBatch(true); err != nil {
				summary.BytesRead = counting.n
				return summary, err
			}
		}
	}
	if err := flushBatch(false); err != nil {
		summary.BytesRead = counting.n
		return summary, err
	}
	if err := writer.Flush(); err != nil {
		summary.BytesRead = counting.n
		return summary, err
	}
	summary.BytesRead = counting.n
	if opts.Progress != nil {
		opts.Progress(SQLConvertProgress{
			RecordsRead: summary.RecordsRead,
			RowsWritten: summary.RowsWritten,
			BytesRead:   summary.BytesRead,
		})
	}
	return summary, nil
}

func PreviewSQLConversion(r io.Reader, opts SQLPreviewOptions) (SQLPreviewReport, error) {
	return PreviewSQLConversionContext(context.Background(), r, opts)
}

func PreviewSQLConversionContext(ctx context.Context, r io.Reader, opts SQLPreviewOptions) (SQLPreviewReport, error) {
	if r == nil {
		return SQLPreviewReport{}, errors.New("reader is required")
	}
	convertOpts, maxBytes, maxRows, err := normalizeSQLPreviewOptions(opts)
	if err != nil {
		return SQLPreviewReport{}, err
	}
	sample, err := readBoundedSampleResult(ctx, r, maxBytes)
	if err != nil {
		return SQLPreviewReport{}, err
	}
	data := sample.Data
	partialOmitted := false
	if sample.Truncated {
		data, partialOmitted, err = CompleteRecordPrefix(ctx, data, convertOpts.Delimiter, convertOpts.MaxRecordBytes, true)
		if err != nil {
			return SQLPreviewReport{}, err
		}
	}

	report := SQLPreviewReport{
		BytesScanned:    sample.BytesScanned,
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

	limitedInput, rowsPreviewed, err := limitedSQLPreviewInput(ctx, data, convertOpts.Delimiter, convertOpts.HasHeader, maxRows, convertOpts.MaxRecordBytes)
	if err != nil {
		return report, err
	}
	report.RowsPreviewed = rowsPreviewed
	if rowsPreviewed == 0 {
		report.Warnings = append(report.Warnings, "no data rows found in bounded sample")
	} else if rowsPreviewed == maxRows {
		report.Warnings = append(report.Warnings, fmt.Sprintf("preview limited to %d data rows", maxRows))
	}
	if convertOpts.IncludeCreateTable && len(convertOpts.ColumnTypes) == 0 {
		inferred, err := InferSchemaContext(ctx, bytes.NewReader(data), SchemaOptions{
			Delimiter:      convertOpts.Delimiter,
			HasHeader:      convertOpts.HasHeader,
			NullValues:     convertOpts.NullValues,
			MaxBytes:       int64(len(data)),
			MaxRows:        maxRows,
			MaxRecordBytes: convertOpts.MaxRecordBytes,
		})
		if err != nil {
			return report, err
		}
		if err := validateInferredSQLSchemaColumns(inferred.Columns); err != nil {
			return report, err
		}
		names, types := schemaColumnsForSQL(inferred.Columns)
		if len(convertOpts.Columns) == 0 {
			convertOpts.Columns = names
		}
		convertOpts.ColumnTypes = types
	}

	var out strings.Builder
	summary, err := ConvertToSQL(ctx, strings.NewReader(limitedInput), &out, convertOpts)
	if err != nil {
		return report, err
	}
	report.SQL = out.String()
	report.Summary = summary
	return report, nil
}

func normalizeSQLPreviewOptions(opts SQLPreviewOptions) (SQLConvertOptions, int64, int, error) {
	maxBytes, err := normalizeSampleByteLimit("SQL preview sample", opts.MaxBytes, DefaultPreviewMaxBytes)
	if err != nil {
		return SQLConvertOptions{}, 0, 0, err
	}
	maxRows, err := normalizeSampleRowLimit("SQL preview sample", opts.MaxRows, DefaultPreviewMaxRows)
	if err != nil {
		return SQLConvertOptions{}, 0, 0, err
	}
	convertOpts := opts.SQLConvertOptions
	convertOpts.Progress = nil
	normalized, err := normalizeSQLConvertOptions(convertOpts)
	if err != nil {
		return SQLConvertOptions{}, 0, 0, err
	}
	return normalized, maxBytes, maxRows, nil
}

func limitedSQLPreviewInput(ctx context.Context, data []byte, delimiter rune, hasHeader bool, maxRows int, maxRecordBytes int64) (string, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reader, err := newBoundedCSVReader(ctx, bytes.NewReader(data), csvReaderConfig{
		Delimiter: delimiter, MaxRecordBytes: maxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return "", 0, err
	}

	var out strings.Builder
	writer := stdcsv.NewWriter(&out)
	writer.Comma = delimiter

	if hasHeader {
		header, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", 0, nil
			}
			return "", 0, err
		}
		if err := writer.Write(header); err != nil {
			return "", 0, err
		}
	}

	rows := 0
	for rows < maxRows {
		if err := ctx.Err(); err != nil {
			return "", rows, err
		}
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", rows, err
		}
		if err := writer.Write(record); err != nil {
			return "", rows, err
		}
		rows++
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", rows, err
	}
	return out.String(), rows, nil
}

func schemaColumnsForSQL(columns []SchemaColumn) ([]string, []string) {
	names := make([]string, len(columns))
	types := make([]string, len(columns))
	for i, column := range columns {
		names[i] = column.Name
		types[i] = column.SQLType
	}
	return names, types
}

func validateInferredSQLSchemaColumns(columns []SchemaColumn) error {
	if err := validateTransformColumnMappingCount("CSV-to-SQL inferred schema", len(columns), true); err != nil {
		return err
	}
	configStringBytes := 0
	for _, column := range columns {
		if err := addTransformConfigString(&configStringBytes, "CSV-to-SQL inferred schema", column.Name); err != nil {
			return err
		}
		if err := addTransformConfigString(&configStringBytes, "CSV-to-SQL inferred schema", column.SQLType); err != nil {
			return err
		}
	}
	return nil
}

func FormatSQLPreviewReport(report SQLPreviewReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Rows previewed: %d\n", report.RowsPreviewed)
	fmt.Fprintf(&b, "Bytes scanned: %d\n", report.BytesScanned)
	if report.Summary.Columns > 0 {
		fmt.Fprintf(&b, "Columns: %d\n", report.Summary.Columns)
	}
	if report.Summary.CreateTableWritten {
		b.WriteString("CREATE TABLE: included\n")
	}
	if report.TruncatedSample {
		b.WriteString("Sample: truncated to bounded preview window\n")
	}
	if len(report.Warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		for _, warning := range report.Warnings {
			b.WriteString("- ")
			b.WriteString(warning)
			b.WriteByte('\n')
		}
	}
	b.WriteString("\nSQL preview:\n")
	if strings.TrimSpace(report.SQL) == "" {
		b.WriteString("(no SQL generated from the bounded sample)\n")
		return b.String()
	}
	b.WriteString(report.SQL)
	return b.String()
}

// ConvertToSQLReaderAt converts a stable random-access source. A fresh section
// reader is used for schema inference and conversion so inference never changes
// the conversion start position. Source-generation enforcement belongs to the
// ReaderAt implementation (the FileService supplies a block-verified reader).
func ConvertToSQLReaderAt(ctx context.Context, source io.ReaderAt, size int64, w io.Writer, opts SQLConvertOptions) (SQLConvertSummary, error) {
	if source == nil {
		return SQLConvertSummary{}, errors.New("reader-at source is required")
	}
	if size < 0 {
		return SQLConvertSummary{}, errors.New("source size is negative")
	}
	if w == nil {
		return SQLConvertSummary{}, errors.New("writer is required")
	}
	normalized, err := normalizeSQLConvertOptions(opts)
	if err != nil {
		return SQLConvertSummary{}, err
	}
	opts = normalized
	if opts.IncludeCreateTable && len(opts.ColumnTypes) == 0 {
		inferred, err := inferSQLSchema(io.NewSectionReader(source, 0, size), opts)
		if err != nil {
			return SQLConvertSummary{}, err
		}
		if len(opts.Columns) == 0 {
			opts.Columns = inferred.columns
		}
		opts.ColumnTypes = inferred.types
		normalized, err = normalizeSQLConvertOptions(opts)
		if err != nil {
			return SQLConvertSummary{}, err
		}
		opts = normalized
	}
	return ConvertToSQL(ctx, io.NewSectionReader(source, 0, size), w, opts)
}

func ConvertToSQLFile(ctx context.Context, inputPath, outputPath string, opts SQLConvertOptions) (_ SQLConvertSummary, retErr error) {
	if inputPath == "" {
		return SQLConvertSummary{}, errors.New("input path is required")
	}
	if outputPath == "" {
		return SQLConvertSummary{}, errors.New("output path is required")
	}
	normalized, err := normalizeSQLConvertOptions(opts)
	if err != nil {
		return SQLConvertSummary{}, err
	}
	opts = normalized

	input, err := regularfile.Open(inputPath)
	if err != nil {
		return SQLConvertSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, input.Close()) }()
	sourceInfo, err := input.Stat()
	if err != nil {
		return SQLConvertSummary{}, err
	}

	output, err := fileio.OpenAtomicOutput(outputPath, []string{inputPath}, 0o600)
	if err != nil {
		return SQLConvertSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, output.Cleanup()) }()

	summary, err := ConvertToSQLReaderAt(ctx, input, sourceInfo.Size(), output, opts)
	if err != nil {
		return summary, err
	}
	if err := validateCSVSourceFile(inputPath, input, sourceInfo); err != nil {
		return summary, err
	}
	if err := output.CommitContext(ctx); err != nil {
		return summary, err
	}
	return summary, nil
}

func normalizeSQLConvertOptions(opts SQLConvertOptions) (SQLConvertOptions, error) {
	if err := validateSQLTransformConfig(opts); err != nil {
		return opts, err
	}
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if !validProjectDelimiter(opts.Delimiter) {
		return opts, fmt.Errorf("invalid delimiter %q", opts.Delimiter)
	}
	if err := validateSQLIdentifierByteLength("table name", opts.TableName); err != nil {
		return opts, err
	}
	opts.TableName = strings.TrimSpace(opts.TableName)
	if opts.TableName == "" {
		return opts, errors.New("table name is required")
	}
	if opts.InsertBatchSize < 0 {
		return opts, fmt.Errorf("SQL insert batch size %d is negative", opts.InsertBatchSize)
	}
	if opts.InsertBatchSize == 0 {
		opts.InsertBatchSize = DefaultSQLInsertBatchSize
	}
	if opts.InsertBatchSize > MaxSQLInsertBatchSize {
		return opts, fmt.Errorf("SQL insert batch size %d exceeds maximum %d", opts.InsertBatchSize, MaxSQLInsertBatchSize)
	}
	if opts.MaxBatchBytes < 0 {
		return opts, fmt.Errorf("SQL insert batch byte limit %d is negative", opts.MaxBatchBytes)
	}
	if opts.MaxBatchBytes == 0 {
		opts.MaxBatchBytes = MaxSQLInsertBatchBytes
	}
	if opts.MaxBatchBytes > MaxSQLInsertBatchBytes {
		return opts, fmt.Errorf("SQL insert batch byte limit %d exceeds application maximum %d", opts.MaxBatchBytes, MaxSQLInsertBatchBytes)
	}
	if opts.InsertMode == "" {
		opts.InsertMode = SQLInsertModeInsert
	}
	switch opts.InsertMode {
	case SQLInsertModeInsert, SQLInsertModeInsertIgnore, SQLInsertModeReplace:
	default:
		return opts, fmt.Errorf("unsupported SQL insert mode %q", opts.InsertMode)
	}
	if opts.Dialect == "" {
		opts.Dialect = SQLDialectMySQL
	}
	if opts.Dialect != SQLDialectMySQL {
		return opts, fmt.Errorf("unsupported SQL dialect %q; current CSV conversion emits MySQL-compatible SQL", opts.Dialect)
	}
	if opts.OnInvalidValue == "" {
		opts.OnInvalidValue = InvalidValueFail
	}
	switch opts.OnInvalidValue {
	case InvalidValueFail, InvalidValueSkipRow, InvalidValueReplace:
	default:
		return opts, fmt.Errorf("unsupported invalid-value policy %q", opts.OnInvalidValue)
	}
	if opts.MaxFieldBytes < 0 {
		return opts, fmt.Errorf("SQL field byte limit %d is negative", opts.MaxFieldBytes)
	}
	if opts.MaxFieldBytes == 0 {
		opts.MaxFieldBytes = DefaultSQLMaxFieldBytes
	}
	limit, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes)
	if err != nil {
		return opts, err
	}
	opts.MaxRecordBytes = limit
	for i, column := range opts.Columns {
		if err := validateSQLIdentifierByteLength(fmt.Sprintf("column %d", i+1), column); err != nil {
			return opts, err
		}
	}
	opts.Columns = normalizeSQLColumnNames(opts.Columns)
	if err := validateSQLIdentifier("table name", opts.TableName); err != nil {
		return opts, err
	}
	if err := validateSQLIdentifiers("column", opts.Columns); err != nil {
		return opts, err
	}
	normalizedTypes, err := normalizeSQLColumnTypes(opts.ColumnTypes)
	if err != nil {
		return opts, err
	}
	opts.ColumnTypes = normalizedTypes
	if len(opts.Columns) > 0 && len(opts.ColumnTypes) > 0 && len(opts.ColumnTypes) != len(opts.Columns) {
		return opts, fmt.Errorf("column type mapping has %d types, expected %d", len(opts.ColumnTypes), len(opts.Columns))
	}
	if len(opts.Columns) > 0 && len(opts.SourceColumns) > 0 && len(opts.SourceColumns) != len(opts.Columns) {
		return opts, fmt.Errorf("source-column mapping has %d positions, expected %d", len(opts.SourceColumns), len(opts.Columns))
	}
	return opts, nil
}

// validateSQLTransformConfig rejects hostile collection shapes and aggregate
// string state before normalization allocates copied slices or lookup maps.
func validateSQLTransformConfig(opts SQLConvertOptions) error {
	if err := validateTransformColumnMappingCount("CSV-to-SQL columns", len(opts.Columns), false); err != nil {
		return err
	}
	if err := validateTransformColumnMappingCount("CSV-to-SQL column types", len(opts.ColumnTypes), false); err != nil {
		return err
	}
	if err := validateTransformColumnMappingCount("CSV-to-SQL source columns", len(opts.SourceColumns), false); err != nil {
		return err
	}
	if err := validateTransformColumnMappingCount("CSV-to-SQL null sentinels", len(opts.NullValues), false); err != nil {
		return err
	}
	if err := validateDistinctNonNegativeIndexes("source column", opts.SourceColumns); err != nil {
		return err
	}

	configStringBytes := 0
	for _, value := range []string{
		opts.TableName,
		string(opts.Dialect),
		string(opts.InsertMode),
		string(opts.OnInvalidValue),
	} {
		if err := addTransformConfigString(&configStringBytes, "CSV-to-SQL", value); err != nil {
			return err
		}
	}
	for _, values := range [][]string{opts.Columns, opts.ColumnTypes, opts.NullValues} {
		for _, value := range values {
			if err := addTransformConfigString(&configStringBytes, "CSV-to-SQL", value); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateSQLConvertOptions validates public configuration without opening an
// input, output, or save dialog.
func ValidateSQLConvertOptions(opts SQLConvertOptions) error {
	_, err := normalizeSQLConvertOptions(opts)
	return err
}

func normalizeSQLColumnNames(columns []string) []string {
	bases := make([]string, len(columns))
	remaining := make(map[string]int, len(columns))
	for i, column := range columns {
		column = strings.TrimSpace(column)
		if column == "" {
			column = fmt.Sprintf("column_%d", i+1)
		}
		bases[i] = column
		remaining[foldSQLIdentifier(column)]++
	}

	normalized := make([]string, len(columns))
	used := make(map[string]struct{}, len(columns))
	nextSuffix := make(map[string]int, len(columns))
	for i, base := range bases {
		baseKey := foldSQLIdentifier(base)
		remaining[baseKey]--
		candidate := base
		candidateKey := baseKey
		if _, exists := used[candidateKey]; exists {
			suffix := nextSuffix[baseKey]
			if suffix < 2 {
				suffix = 2
			}
			for {
				candidate = sqlIdentifierWithSuffix(base, suffix)
				candidateKey = foldSQLIdentifier(candidate)
				suffix++
				_, alreadyUsed := used[candidateKey]
				if !alreadyUsed && remaining[candidateKey] == 0 {
					break
				}
			}
			nextSuffix[baseKey] = suffix
		}
		used[candidateKey] = struct{}{}
		normalized[i] = candidate
	}
	return normalized
}

func foldSQLIdentifier(identifier string) string {
	return cases.Fold().String(identifier)
}

func sqlIdentifierWithSuffix(base string, sequence int) string {
	suffix := fmt.Sprintf("_%d", sequence)
	baseRunes := []rune(base)
	maxBaseRunes := MaxSQLIdentifierRunes - utf8.RuneCountInString(suffix)
	if maxBaseRunes > 0 && len(baseRunes) > maxBaseRunes {
		base = string(baseRunes[:maxBaseRunes])
	}
	return base + suffix
}

func makeSQLNullSet(values []string) map[string]struct{} {
	nulls := make(map[string]struct{}, len(values))
	for _, value := range values {
		nulls[value] = struct{}{}
	}
	return nulls
}

func normalizeSQLColumnTypes(types []string) ([]string, error) {
	if len(types) == 0 {
		return nil, nil
	}
	normalized := make([]string, len(types))
	for i, columnType := range types {
		switch strings.ToUpper(strings.TrimSpace(columnType)) {
		case SQLTypeBigInt:
			normalized[i] = SQLTypeBigInt
		case SQLTypeDouble:
			normalized[i] = SQLTypeDouble
		case SQLTypeBoolean:
			normalized[i] = SQLTypeBoolean
		case SQLTypeText:
			normalized[i] = SQLTypeText
		default:
			return nil, fmt.Errorf("invalid SQL column type %q at position %d", columnType, i)
		}
	}
	return normalized, nil
}

func resolveSQLColumnTypes(types []string, columns int) ([]string, error) {
	if len(types) == 0 {
		resolved := make([]string, columns)
		for i := range resolved {
			resolved[i] = SQLTypeText
		}
		return resolved, nil
	}
	if len(types) != columns {
		return nil, fmt.Errorf("column type mapping has %d types, expected %d", len(types), columns)
	}
	return append([]string(nil), types...), nil
}

// errInvalidSQLValue marks a field that can't be emitted safely; the policy
// decides whether that fails the job, skips the row, or sanitizes the value.
var errInvalidSQLValue = errors.New("field contains an unsafe byte")

// sqlValuesTuple builds the VALUES tuple for a record. Under InvalidValueSkipRow
// it returns skip=true when any field is unsafe; under InvalidValueReplace it
// returns sanitized=true when any field was repaired; otherwise an unsafe field
// is a fatal error.
func sqlValuesTuple(record []string, nulls map[string]struct{}, columnTypes []string, policy InvalidValuePolicy) (tuple string, skip bool, sanitized bool, err error) {
	if len(record) != len(columnTypes) {
		return "", false, false, fmt.Errorf("record has %d fields but type mapping has %d", len(record), len(columnTypes))
	}
	values := make([]string, len(record))
	for i, value := range record {
		if _, ok := nulls[value]; ok {
			values[i] = "NULL"
			continue
		}
		if columnTypes[i] == SQLTypeBoolean {
			// Byte-repair policies do not authorize semantic type coercion.
			literal, boolErr := mysqlBooleanLiteral(value)
			if boolErr != nil {
				return "", false, false, fmt.Errorf("field %d: %w", i+1, boolErr)
			}
			values[i] = literal
			continue
		}
		quoted, repaired, qerr := quoteSQLValue(value, policy)
		if qerr != nil {
			if policy == InvalidValueSkipRow {
				return "", true, false, nil
			}
			return "", false, false, fmt.Errorf("field %d: %w", i+1, qerr)
		}
		sanitized = sanitized || repaired
		values[i] = quoted
	}
	return "(" + strings.Join(values, ", ") + ")", false, sanitized, nil
}

func mysqlBooleanLiteral(value string) (string, error) {
	switch {
	case value == "1", strings.EqualFold(value, "true"):
		return "1", nil
	case value == "0", strings.EqualFold(value, "false"):
		return "0", nil
	default:
		return "", fmt.Errorf("value %q is not a MySQL BOOLEAN; expected true, false, 1, 0, or an exact configured NULL token", value)
	}
}

func validateSQLRecordFieldSizes(record []string, maxFieldBytes int64) error {
	if maxFieldBytes <= 0 {
		return nil
	}
	for i, value := range record {
		if int64(len(value)) > maxFieldBytes {
			return fmt.Errorf("field %d is %d bytes, exceeding max field size %d", i+1, len(value), maxFieldBytes)
		}
	}
	return nil
}

func sqlInsertBatchHeader(mode SQLInsertMode, table string, columns []string) (string, error) {
	if len(columns) == 0 {
		return "", errors.New("columns are required for SQL INSERT")
	}
	verb, err := sqlInsertVerb(mode)
	if err != nil {
		return "", err
	}
	quotedColumns := make([]string, len(columns))
	for i, column := range columns {
		quoted, err := quoteSQLIdentifier(column)
		if err != nil {
			return "", err
		}
		quotedColumns[i] = quoted
	}
	quotedTable, err := quoteSQLIdentifier(table)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s (%s) VALUES\n  ", verb, quotedTable, strings.Join(quotedColumns, ", ")), nil
}

func writeSQLInsertBatch(w io.Writer, header string, tuples []string) error {
	if len(tuples) == 0 {
		return nil
	}
	if w == nil {
		return errors.New("SQL INSERT writer is required")
	}
	if err := writeFullSQLString(w, header); err != nil {
		return err
	}
	for i, tuple := range tuples {
		if i > 0 {
			if err := writeFullSQLString(w, sqlInsertTupleSeparator); err != nil {
				return err
			}
		}
		if err := writeFullSQLString(w, tuple); err != nil {
			return err
		}
	}
	return writeFullSQLString(w, sqlInsertTerminator)
}

func writeFullSQLString(w io.Writer, value string) error {
	for len(value) > 0 {
		n, err := io.WriteString(w, value)
		if n < 0 || n > len(value) {
			return fmt.Errorf("SQL writer returned invalid byte count %d for %d bytes", n, len(value))
		}
		value = value[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func sqlInsertVerb(mode SQLInsertMode) (string, error) {
	switch mode {
	case SQLInsertModeInsert:
		return "INSERT INTO", nil
	case SQLInsertModeInsertIgnore:
		return "INSERT IGNORE INTO", nil
	case SQLInsertModeReplace:
		return "REPLACE INTO", nil
	default:
		return "", fmt.Errorf("unsupported SQL insert mode %q", mode)
	}
}

func writeSQLCreateTable(w io.Writer, table string, columns []string, types []string) error {
	if len(columns) == 0 {
		return errors.New("columns are required for CREATE TABLE")
	}
	if len(types) != len(columns) {
		return fmt.Errorf("column type mapping has %d types, expected %d", len(types), len(columns))
	}
	lines := make([]string, len(columns))
	for i, column := range columns {
		quoted, err := quoteSQLIdentifier(column)
		if err != nil {
			return err
		}
		lines[i] = fmt.Sprintf("  %s %s", quoted, types[i])
	}
	quotedTable, err := quoteSQLIdentifier(table)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "CREATE TABLE IF NOT EXISTS %s (\n%s\n);\n", quotedTable, strings.Join(lines, ",\n"))
	return err
}

type inferredSQLSchema struct {
	columns []string
	types   []string
}

func inferSQLSchema(input io.Reader, opts SQLConvertOptions) (inferredSQLSchema, error) {
	report, err := InferSchema(input, SchemaOptions{
		Delimiter:      opts.Delimiter,
		HasHeader:      opts.HasHeader,
		NullValues:     opts.NullValues,
		MaxRecordBytes: opts.MaxRecordBytes,
	})
	if err != nil {
		return inferredSQLSchema{}, err
	}
	if err := validateInferredSQLSchemaColumns(report.Columns); err != nil {
		return inferredSQLSchema{}, err
	}
	columns, types := schemaColumnsForSQL(report.Columns)
	return inferredSQLSchema{columns: columns, types: types}, nil
}

func quoteSQLIdentifier(identifier string) (string, error) {
	if err := validateSQLIdentifier("SQL identifier", identifier); err != nil {
		return "", err
	}
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`", nil
}

// quoteSQLString quotes a value, failing on any unsafe byte (the strict default).
func quoteSQLString(value string) (string, error) {
	out, _, err := quoteSQLValue(value, InvalidValueFail)
	return out, err
}

// quoteSQLValue emits a MySQL value without depending on
// NO_BACKSLASH_ESCAPES or the importing connection character set. Printable
// ASCII uses apostrophe doubling. Backslashes, supported controls, and
// non-ASCII UTF-8 use an ASCII-only hex literal with explicit utf8mb4 decoding.
func quoteSQLValue(value string, policy InvalidValuePolicy) (out string, sanitized bool, err error) {
	var b strings.Builder
	b.Grow(len(value))
	requiresHex := false
	for i := 0; i < len(value); {
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 1 {
			if policy == InvalidValueReplace {
				b.WriteRune('\uFFFD')
				sanitized = true
				requiresHex = true
				i++
				continue
			}
			return "", false, fmt.Errorf("invalid UTF-8 byte 0x%02X cannot be emitted safely: %w", value[i], errInvalidSQLValue)
		}
		switch r {
		case 0, '\b', '\t', '\n', '\r', 0x1A, '\\':
			requiresHex = true
			b.WriteString(value[i : i+size])
		default:
			if r < 0x20 || r == 0x7F {
				if policy == InvalidValueReplace {
					b.WriteRune('\uFFFD')
					sanitized = true
					requiresHex = true
					i += size
					continue
				}
				return "", false, fmt.Errorf("control character U+%04X cannot be emitted safely: %w", r, errInvalidSQLValue)
			}
			if r > 0x7F {
				requiresHex = true
			}
			b.WriteString(value[i : i+size])
		}
		i += size
	}
	normalized := b.String()
	if requiresHex {
		return fmt.Sprintf("CONVERT(X'%X' USING utf8mb4)", []byte(normalized)), sanitized, nil
	}
	return "'" + strings.ReplaceAll(normalized, "'", "''") + "'", sanitized, nil
}

func validateSQLIdentifiers(label string, identifiers []string) error {
	seen := make(map[string]int, len(identifiers))
	for i, identifier := range identifiers {
		if err := validateSQLIdentifier(fmt.Sprintf("%s %d", label, i+1), identifier); err != nil {
			return err
		}
		key := foldSQLIdentifier(identifier)
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("%s %d duplicates %s %d under MySQL case-insensitive identifier rules", label, i+1, label, previous+1)
		}
		seen[key] = i
	}
	return nil
}

func validateSQLIdentifier(label, identifier string) error {
	if err := validateSQLIdentifierByteLength(label, identifier); err != nil {
		return err
	}
	if !utf8.ValidString(identifier) {
		return fmt.Errorf("%s contains invalid UTF-8", label)
	}
	if strings.TrimSpace(identifier) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if runes := utf8.RuneCountInString(identifier); runes > MaxSQLIdentifierRunes {
		return fmt.Errorf("%s is %d characters; MySQL identifiers are limited to %d", label, runes, MaxSQLIdentifierRunes)
	}
	for _, r := range identifier {
		if r == 0 || r < 0x20 || r == 0x7F {
			return fmt.Errorf("%s contains unsupported control character U+%04X", label, r)
		}
	}
	return nil
}

func validateSQLIdentifierByteLength(label, identifier string) error {
	if len(identifier) > MaxSQLIdentifierBytes {
		return fmt.Errorf("%s exceeds the %d-byte SQL identifier input limit", label, MaxSQLIdentifierBytes)
	}
	return nil
}

func validateSQLSourceColumnRange(sourceColumns []int, inputColumns int) error {
	for position, sourceColumn := range sourceColumns {
		if sourceColumn >= inputColumns {
			return fmt.Errorf("source column %d at position %d is out of range for a %d-field input record", sourceColumn, position, inputColumns)
		}
	}
	return nil
}

func validateCSVSourceFile(path string, input *os.File, expected os.FileInfo) error {
	if input == nil || expected == nil {
		return errors.New("opened CSV source identity is required")
	}
	opened, err := input.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened CSV source: %w", err)
	}
	current, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect CSV source path: %w", err)
	}
	if !opened.Mode().IsRegular() || !current.Mode().IsRegular() ||
		!os.SameFile(expected, opened) || !os.SameFile(opened, current) ||
		opened.Size() != expected.Size() || !opened.ModTime().Equal(expected.ModTime()) {
		return errors.New("CSV source changed during conversion")
	}
	return nil
}
