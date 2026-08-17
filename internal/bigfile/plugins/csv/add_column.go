package csv

import (
	"bufio"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/regularfile"
)

// MaxAddColumnPosition bounds per-record padding and guarantees that padding
// plus the inserted field cannot exceed the shared CSV width cap. Position is
// a zero-based insertion index.
const MaxAddColumnPosition = MaxCSVFieldsPerRecord - 1

// AddColumnOptions inserts Value at Position in every logical record. Records
// shorter than Position are padded with FillValue. Records wider than Position
// retain every original field, shifted one place to the right.
type AddColumnOptions struct {
	Delimiter      rune
	Position       int
	Value          string
	FillValue      string
	MaxRecordBytes int64
	Progress       func(AddColumnProgress)
}

type AddColumnProgress struct {
	RecordsRead    int64
	RecordsWritten int64
}

type AddColumnSummary struct {
	RecordsRead    int64
	RecordsWritten int64
	Position       int
	Delimiter      rune
}

// ValidateAddColumnOptions rejects allocation-driving configuration before an
// input or output artifact is opened.
func ValidateAddColumnOptions(opts AddColumnOptions) error {
	configStringBytes := 0
	if err := addTransformConfigString(&configStringBytes, "CSV add-column", opts.Value); err != nil {
		return err
	}
	if err := addTransformConfigString(&configStringBytes, "CSV add-column", opts.FillValue); err != nil {
		return err
	}
	if err := ValidateDelimiter(opts.Delimiter); err != nil {
		return err
	}
	if opts.Position < 0 {
		return fmt.Errorf("add-column position %d is negative", opts.Position)
	}
	if opts.Position > MaxAddColumnPosition {
		return fmt.Errorf("add-column position %d exceeds maximum %d", opts.Position, MaxAddColumnPosition)
	}
	if _, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes); err != nil {
		return err
	}
	return nil
}

// AddColumn streams logical records without retaining whole-file state. It
// uses strict quote parsing, preserves every existing field, and pads only
// genuinely short records.
func AddColumn(ctx context.Context, r io.Reader, w io.Writer, opts AddColumnOptions) (AddColumnSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return AddColumnSummary{}, errors.New("reader is required")
	}
	if w == nil {
		return AddColumnSummary{}, errors.New("writer is required")
	}
	if err := ValidateAddColumnOptions(opts); err != nil {
		return AddColumnSummary{}, err
	}

	br := bufio.NewReader(r)
	if err := skipInputBOM(br); err != nil {
		return AddColumnSummary{}, err
	}
	reader, err := newBoundedCSVReader(ctx, br, csvReaderConfig{
		Delimiter: opts.Delimiter, MaxRecordBytes: opts.MaxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return AddColumnSummary{}, err
	}
	writer := stdcsv.NewWriter(w)
	writer.Comma = opts.Delimiter

	summary := AddColumnSummary{Position: opts.Position, Delimiter: opts.Delimiter}
	for {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return summary, fmt.Errorf("record %d: %w", summary.RecordsRead+1, err)
		}
		summary.RecordsRead++
		outputFields := len(record) + 1
		if opts.Position >= len(record) {
			outputFields = opts.Position + 1
		}
		if outputFields > MaxCSVFieldsPerRecord {
			return summary, fmt.Errorf(
				"record %d would contain %d fields after add-column; maximum is %d",
				summary.RecordsRead, outputFields, MaxCSVFieldsPerRecord,
			)
		}
		output := addColumnRecord(record, opts.Position, opts.Value, opts.FillValue)
		if err := validateCSVOutputRecordSize("CSV add-column output record", output, opts.Delimiter); err != nil {
			return summary, fmt.Errorf("record %d: %w", summary.RecordsRead, err)
		}
		if err := writer.Write(output); err != nil {
			return summary, err
		}
		summary.RecordsWritten++
		if opts.Progress != nil {
			opts.Progress(AddColumnProgress{
				RecordsRead: summary.RecordsRead, RecordsWritten: summary.RecordsWritten,
			})
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return summary, err
	}
	return summary, ctx.Err()
}

// AddColumnFile publishes only a complete output and refuses to replace an
// existing destination or any path alias of the source.
func AddColumnFile(ctx context.Context, inputPath, outputPath string, opts AddColumnOptions) (_ AddColumnSummary, retErr error) {
	if inputPath == "" {
		return AddColumnSummary{}, errors.New("input path is required")
	}
	if outputPath == "" {
		return AddColumnSummary{}, errors.New("output path is required")
	}
	if err := ValidateAddColumnOptions(opts); err != nil {
		return AddColumnSummary{}, err
	}
	input, err := regularfile.Open(inputPath)
	if err != nil {
		return AddColumnSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, input.Close()) }()

	output, err := fileio.OpenAtomicOutput(outputPath, []string{inputPath}, 0o600)
	if err != nil {
		return AddColumnSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, output.Cleanup()) }()

	summary, err := AddColumn(ctx, input, output, opts)
	if err != nil {
		return summary, err
	}
	if err := output.CommitContext(ctx); err != nil {
		return summary, err
	}
	return summary, nil
}

func addColumnRecord(record []string, position int, value, fillValue string) []string {
	outputFields := len(record) + 1
	if position >= len(record) {
		outputFields = position + 1
	}
	output := make([]string, outputFields)
	if position < len(record) {
		copy(output, record[:position])
		output[position] = value
		copy(output[position+1:], record[position:])
		return output
	}
	copy(output, record)
	for i := len(record); i < position; i++ {
		output[i] = fillValue
	}
	output[position] = value
	return output
}
