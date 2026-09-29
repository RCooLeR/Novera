package csv

import (
	"bufio"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/regularfile"
)

// TransformSummary is the result of a streaming row transform.
type TransformSummary struct {
	RecordsRead    int64
	RecordsWritten int64
	Delimiter      rune
}

type rowTransform struct {
	reader *stdcsv.Reader
	writer *stdcsv.Writer
	bw     *bufio.Writer
	in     *os.File
	out    *fileio.AtomicOutput
}

// tempOutputPath is the legacy predictable scratch name retained for
// containment regressions. Current operations use an operation-owned random
// sibling managed by fileio.AtomicOutput.
func tempOutputPath(dst string) string { return dst + ".quarry-part" }

func newRowStream(ctx context.Context, r io.Reader, w io.Writer, delim rune, maxRecordBytes int64) (*stdcsv.Reader, *stdcsv.Writer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return nil, nil, errors.New("reader is required")
	}
	if w == nil {
		return nil, nil, errors.New("writer is required")
	}
	if delim == 0 {
		delim = ','
	}
	if err := ValidateDelimiter(delim); err != nil {
		return nil, nil, err
	}
	limit, err := normalizeLogicalRecordLimit(maxRecordBytes)
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReader(r)
	if err := skipInputBOM(br); err != nil {
		return nil, nil, err
	}
	reader, err := newBoundedCSVReader(ctx, br, csvReaderConfig{
		Delimiter: delim, MaxRecordBytes: limit,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return nil, nil, err
	}
	writer := stdcsv.NewWriter(w)
	writer.Comma = delim
	return reader, writer, nil
}

func finishRowStream(writer *stdcsv.Writer) error {
	writer.Flush()
	return writer.Error()
}

func openRowTransform(ctx context.Context, srcPath, dstPath string, delim rune, maxRecordBytes int64) (*rowTransform, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if delim == 0 {
		delim = ','
	}
	if err := ValidateDelimiter(delim); err != nil {
		return nil, err
	}
	limit, err := normalizeLogicalRecordLimit(maxRecordBytes)
	if err != nil {
		return nil, err
	}
	in, err := regularfile.Open(srcPath)
	if err != nil {
		return nil, err
	}
	out, err := fileio.OpenAtomicOutput(dstPath, []string{srcPath}, 0o600)
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	br := bufio.NewReader(in)
	if err := skipInputBOM(br); err != nil {
		_ = in.Close()
		_ = out.Cleanup()
		return nil, err
	}
	reader, err := newBoundedCSVReader(ctx, br, csvReaderConfig{
		Delimiter: delim, MaxRecordBytes: limit,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		_ = in.Close()
		_ = out.Cleanup()
		return nil, err
	}
	bw := bufio.NewWriter(out)
	writer := stdcsv.NewWriter(bw)
	writer.Comma = delim
	return &rowTransform{reader: reader, writer: writer, bw: bw, in: in, out: out}, nil
}

func (t *rowTransform) finish(ctx context.Context) error {
	t.writer.Flush()
	if err := t.writer.Error(); err != nil {
		return err
	}
	if err := t.bw.Flush(); err != nil {
		return err
	}
	return t.out.CommitContext(ctx)
}

func (t *rowTransform) close() error {
	return errors.Join(t.in.Close(), t.out.Cleanup())
}

// FilterOptions selects rows where column[Column] matches Op/Value.
type FilterOptions struct {
	Delimiter      rune
	HasHeader      bool
	Column         int
	Op             string // eq | ne | contains | gt | lt | empty | nonempty
	Value          string
	Negate         bool
	MaxRecordBytes int64
	Progress       func(records int64)
}

// ValidateFilterOptions rejects unknown operations and allocation-driving
// configuration before a source or destination is opened. An unknown operation
// must never silently produce an empty file.
func ValidateFilterOptions(opts FilterOptions) error {
	configStringBytes := 0
	if err := addTransformConfigString(&configStringBytes, "CSV filter", opts.Op); err != nil {
		return err
	}
	if err := addTransformConfigString(&configStringBytes, "CSV filter", opts.Value); err != nil {
		return err
	}
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if err := ValidateDelimiter(opts.Delimiter); err != nil {
		return err
	}
	if opts.Column < 0 {
		return fmt.Errorf("filter column %d is negative", opts.Column)
	}
	if _, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes); err != nil {
		return err
	}
	switch opts.Op {
	case "eq", "ne", "contains", "gt", "lt", "empty", "nonempty":
		return nil
	default:
		return fmt.Errorf("invalid filter operation %q", opts.Op)
	}
}

// reportEvery throttles a progress callback to multiples of n records.
func reportEvery(p func(records int64), records, n int64) {
	if p != nil && records%n == 0 {
		p(records)
	}
}

func rowMatches(cell, op, value string) bool {
	switch op {
	case "eq":
		return cell == value
	case "ne":
		return cell != value
	case "contains":
		return strings.Contains(cell, value)
	case "empty":
		return strings.TrimSpace(cell) == ""
	case "nonempty":
		return strings.TrimSpace(cell) != ""
	case "gt", "lt":
		cn, e1 := strconv.ParseFloat(strings.TrimSpace(cell), 64)
		vn, e2 := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if e1 == nil && e2 == nil {
			if op == "gt" {
				return cn > vn
			}
			return cn < vn
		}
		if op == "gt" {
			return cell > value
		}
		return cell < value
	default:
		return false
	}
}

// FilterRowsFile streams src to dst keeping only matching rows (header preserved).
func FilterRowsFile(ctx context.Context, srcPath, dstPath string, opts FilterOptions) (_ TransformSummary, retErr error) {
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if err := ValidateFilterOptions(opts); err != nil {
		return TransformSummary{}, err
	}
	t, err := openRowTransform(ctx, srcPath, dstPath, opts.Delimiter, opts.MaxRecordBytes)
	if err != nil {
		return TransformSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, t.close()) }()
	sum, err := filterRows(ctx, t.reader, t.writer, opts)
	if err != nil {
		return sum, err
	}
	if err := t.finish(ctx); err != nil {
		return sum, err
	}
	return sum, nil
}

// FilterRows streams a strict CSV transform between caller-owned streams.
// Callers that publish a file remain responsible for atomic output and source
// generation validation.
func FilterRows(ctx context.Context, r io.Reader, w io.Writer, opts FilterOptions) (TransformSummary, error) {
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if err := ValidateFilterOptions(opts); err != nil {
		return TransformSummary{}, err
	}
	reader, writer, err := newRowStream(ctx, r, w, opts.Delimiter, opts.MaxRecordBytes)
	if err != nil {
		return TransformSummary{}, err
	}
	sum, err := filterRows(ctx, reader, writer, opts)
	if err != nil {
		return sum, err
	}
	if err := finishRowStream(writer); err != nil {
		return sum, err
	}
	return sum, contextErr(ctx)
}

func filterRows(ctx context.Context, reader *stdcsv.Reader, writer *stdcsv.Writer, opts FilterOptions) (TransformSummary, error) {
	sum := TransformSummary{Delimiter: opts.Delimiter}
	first := true
	for {
		if err := contextErr(ctx); err != nil {
			return sum, err
		}
		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sum, err
		}
		sum.RecordsRead++
		reportEvery(opts.Progress, sum.RecordsRead, 50000)
		if first && opts.HasHeader {
			first = false
			if err := validateCSVOutputRecordSize("filtered CSV header record", rec, opts.Delimiter); err != nil {
				return sum, fmt.Errorf("record %d: %w", sum.RecordsRead, err)
			}
			if err := writer.Write(rec); err != nil {
				return sum, err
			}
			sum.RecordsWritten++
			continue
		}
		first = false
		cell := ""
		if opts.Column >= 0 && opts.Column < len(rec) {
			cell = rec[opts.Column]
		}
		keep := rowMatches(cell, opts.Op, opts.Value)
		if opts.Negate {
			keep = !keep
		}
		if keep {
			if err := validateCSVOutputRecordSize("filtered CSV record", rec, opts.Delimiter); err != nil {
				return sum, fmt.Errorf("record %d: %w", sum.RecordsRead, err)
			}
			if err := writer.Write(rec); err != nil {
				return sum, err
			}
			sum.RecordsWritten++
		}
	}
	return sum, nil
}

// DedupeOptions drops duplicate rows, by the whole row or by a key column.
type DedupeOptions struct {
	Delimiter       rune
	HasHeader       bool
	KeyColumn       int // <0 = dedupe by whole row
	MaxRecordBytes  int64
	MaxDistinctKeys int   // 0 uses DefaultDedupeMaxDistinctKeys
	MaxMemoryBytes  int64 // 0 uses DefaultDedupeMaxMemoryBytes
	Progress        func(records int64)
}

// DedupeRowsFile keeps the first occurrence of each row/key. It is exact within
// validated in-memory cardinality and memory budgets, and fails without
// publishing the destination when either budget would be exceeded.
func DedupeRowsFile(ctx context.Context, srcPath, dstPath string, opts DedupeOptions) (_ TransformSummary, retErr error) {
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	normalized, err := normalizeDedupeOptions(opts)
	if err != nil {
		return TransformSummary{}, err
	}
	opts = normalized
	seen, err := newDedupeSet(opts.MaxDistinctKeys, opts.MaxMemoryBytes)
	if err != nil {
		return TransformSummary{}, err
	}
	t, err := openRowTransform(ctx, srcPath, dstPath, opts.Delimiter, opts.MaxRecordBytes)
	if err != nil {
		return TransformSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, t.close()) }()
	sum, err := dedupeRows(ctx, t.reader, t.writer, opts, seen)
	if err != nil {
		return sum, err
	}
	if err := t.finish(ctx); err != nil {
		return sum, err
	}
	return sum, nil
}

// DedupeRows performs exact bounded deduplication between caller-owned
// streams. It never degrades to probabilistic/hash-only equality.
func DedupeRows(ctx context.Context, r io.Reader, w io.Writer, opts DedupeOptions) (TransformSummary, error) {
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	normalized, err := normalizeDedupeOptions(opts)
	if err != nil {
		return TransformSummary{}, err
	}
	opts = normalized
	seen, err := newDedupeSet(opts.MaxDistinctKeys, opts.MaxMemoryBytes)
	if err != nil {
		return TransformSummary{}, err
	}
	reader, writer, err := newRowStream(ctx, r, w, opts.Delimiter, opts.MaxRecordBytes)
	if err != nil {
		return TransformSummary{}, err
	}
	sum, err := dedupeRows(ctx, reader, writer, opts, seen)
	if err != nil {
		return sum, err
	}
	if err := finishRowStream(writer); err != nil {
		return sum, err
	}
	return sum, contextErr(ctx)
}

func dedupeRows(ctx context.Context, reader *stdcsv.Reader, writer *stdcsv.Writer, opts DedupeOptions, seen *dedupeSet) (TransformSummary, error) {
	sum := TransformSummary{Delimiter: opts.Delimiter}
	first := true
	for {
		if err := contextErr(ctx); err != nil {
			return sum, err
		}
		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sum, err
		}
		sum.RecordsRead++
		reportEvery(opts.Progress, sum.RecordsRead, 50000)
		if err := contextErr(ctx); err != nil {
			return sum, err
		}
		if first && opts.HasHeader {
			first = false
			if err := validateCSVOutputRecordSize("deduplicated CSV header record", rec, opts.Delimiter); err != nil {
				return sum, fmt.Errorf("record %d: %w", sum.RecordsRead, err)
			}
			if err := writer.Write(rec); err != nil {
				return sum, err
			}
			sum.RecordsWritten++
			continue
		}
		first = false
		key, err := dedupeKeyContext(ctx, rec, opts.KeyColumn)
		if err != nil {
			return sum, err
		}
		if err := contextErr(ctx); err != nil {
			return sum, err
		}
		added, err := seen.add(key, sum.RecordsRead)
		if err != nil {
			return sum, err
		}
		if !added {
			continue
		}
		if err := validateCSVOutputRecordSize("deduplicated CSV record", rec, opts.Delimiter); err != nil {
			return sum, fmt.Errorf("record %d: %w", sum.RecordsRead, err)
		}
		if err := writer.Write(rec); err != nil {
			return sum, err
		}
		sum.RecordsWritten++
	}
	return sum, nil
}

// SampleOptions keeps every Nth data row.
type SampleOptions struct {
	Delimiter      rune
	HasHeader      bool
	EveryN         int
	MaxRecordBytes int64
	Progress       func(records int64)
}

// ValidateSampleOptions validates sampling configuration without opening
// files. Non-positive EveryN retains the compatibility default of 10.
func ValidateSampleOptions(opts SampleOptions) error {
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if err := ValidateDelimiter(opts.Delimiter); err != nil {
		return err
	}
	_, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes)
	return err
}

// SampleRowsFile keeps every Nth data row (header preserved).
func SampleRowsFile(ctx context.Context, srcPath, dstPath string, opts SampleOptions) (_ TransformSummary, retErr error) {
	if opts.EveryN <= 0 {
		opts.EveryN = 10
	}
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if err := ValidateSampleOptions(opts); err != nil {
		return TransformSummary{}, err
	}
	t, err := openRowTransform(ctx, srcPath, dstPath, opts.Delimiter, opts.MaxRecordBytes)
	if err != nil {
		return TransformSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, t.close()) }()
	sum, err := sampleRows(ctx, t.reader, t.writer, opts)
	if err != nil {
		return sum, err
	}
	if err := t.finish(ctx); err != nil {
		return sum, err
	}
	return sum, nil
}

// SampleRows streams every Nth data row between caller-owned streams.
func SampleRows(ctx context.Context, r io.Reader, w io.Writer, opts SampleOptions) (TransformSummary, error) {
	if opts.EveryN <= 0 {
		opts.EveryN = 10
	}
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if err := ValidateSampleOptions(opts); err != nil {
		return TransformSummary{}, err
	}
	reader, writer, err := newRowStream(ctx, r, w, opts.Delimiter, opts.MaxRecordBytes)
	if err != nil {
		return TransformSummary{}, err
	}
	sum, err := sampleRows(ctx, reader, writer, opts)
	if err != nil {
		return sum, err
	}
	if err := finishRowStream(writer); err != nil {
		return sum, err
	}
	return sum, contextErr(ctx)
}

func sampleRows(ctx context.Context, reader *stdcsv.Reader, writer *stdcsv.Writer, opts SampleOptions) (TransformSummary, error) {
	sum := TransformSummary{Delimiter: opts.Delimiter}
	first := true
	var dataIdx int64
	for {
		if err := contextErr(ctx); err != nil {
			return sum, err
		}
		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sum, err
		}
		sum.RecordsRead++
		reportEvery(opts.Progress, sum.RecordsRead, 50000)
		if first && opts.HasHeader {
			first = false
			if err := validateCSVOutputRecordSize("sampled CSV header record", rec, opts.Delimiter); err != nil {
				return sum, fmt.Errorf("record %d: %w", sum.RecordsRead, err)
			}
			if err := writer.Write(rec); err != nil {
				return sum, err
			}
			sum.RecordsWritten++
			continue
		}
		first = false
		if dataIdx%int64(opts.EveryN) == 0 {
			if err := validateCSVOutputRecordSize("sampled CSV record", rec, opts.Delimiter); err != nil {
				return sum, fmt.Errorf("record %d: %w", sum.RecordsRead, err)
			}
			if err := writer.Write(rec); err != nil {
				return sum, err
			}
			sum.RecordsWritten++
		}
		dataIdx++
	}
	return sum, nil
}
