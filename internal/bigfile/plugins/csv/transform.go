package csv

import (
	"bufio"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

// TransformSummary is the result of a streaming row transform.
type TransformSummary struct {
	RecordsRead    int64
	RecordsWritten int64
	Delimiter      rune
}

type rowTransform struct {
	reader  *stdcsv.Reader
	writer  *stdcsv.Writer
	bw      *bufio.Writer
	in      *os.File
	out     *os.File
	cleanup bool
	tmpPath string
	dstPath string
}

// tempOutputPath is the scratch file a transform/export writes to before it is
// atomically renamed over the destination on success. Writing to a temp file
// means the Save dialog's confirmed "replace" overwrites the target only when
// the new output completed — a failed/cancelled run never destroys it.
func tempOutputPath(dst string) string { return dst + ".quarry-part" }

func openRowTransform(srcPath, dstPath string, delim rune) (*rowTransform, error) {
	if delim == 0 {
		delim = ','
	}
	if same, err := sameFilePath(srcPath, dstPath); err != nil {
		return nil, err
	} else if same {
		return nil, errors.New("output path must be different from input path")
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	tmpPath := tempOutputPath(dstPath)
	out, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	br := bufio.NewReader(in)
	if err := skipInputBOM(br); err != nil {
		_ = in.Close()
		_ = out.Close()
		_ = os.Remove(tmpPath)
		return nil, err
	}
	reader := stdcsv.NewReader(br)
	reader.Comma = delim
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = false
	bw := bufio.NewWriter(out)
	writer := stdcsv.NewWriter(bw)
	writer.Comma = delim
	return &rowTransform{reader: reader, writer: writer, bw: bw, in: in, out: out, cleanup: true, tmpPath: tmpPath, dstPath: dstPath}, nil
}

func (t *rowTransform) finish() error {
	t.writer.Flush()
	if err := t.writer.Error(); err != nil {
		return err
	}
	if err := t.bw.Flush(); err != nil {
		return err
	}
	if err := t.out.Sync(); err != nil {
		return err
	}
	if err := t.out.Close(); err != nil {
		return err
	}
	if err := os.Rename(t.tmpPath, t.dstPath); err != nil {
		return err
	}
	t.cleanup = false
	return nil
}

func (t *rowTransform) close() {
	_ = t.in.Close()
	if t.cleanup {
		_ = t.out.Close()
		_ = os.Remove(t.tmpPath)
	}
}

// FilterOptions selects rows where column[Column] matches Op/Value.
type FilterOptions struct {
	Delimiter rune
	HasHeader bool
	Column    int
	Op        string // eq | ne | contains | gt | lt | empty | nonempty
	Value     string
	Negate    bool
	Progress  func(records int64)
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
func FilterRowsFile(ctx context.Context, srcPath, dstPath string, opts FilterOptions) (TransformSummary, error) {
	t, err := openRowTransform(srcPath, dstPath, opts.Delimiter)
	if err != nil {
		return TransformSummary{}, err
	}
	defer t.close()
	sum := TransformSummary{Delimiter: opts.Delimiter}
	first := true
	for {
		if err := contextErr(ctx); err != nil {
			return sum, err
		}
		rec, err := t.reader.Read()
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
			if err := t.writer.Write(rec); err != nil {
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
			if err := t.writer.Write(rec); err != nil {
				return sum, err
			}
			sum.RecordsWritten++
		}
	}
	if err := t.finish(); err != nil {
		return sum, err
	}
	return sum, nil
}

// dedupeKey builds a collision-free seen-set key. A leading marker byte
// distinguishes the three cases so they never alias each other:
//   - keyColumn present  → 'k' + the cell
//   - keyColumn missing  (row too short) → 'r' + the full NUL-joined row
//   - whole-row dedupe   → 'w' + the full NUL-joined row
// Without this, a short row (no key cell) and a row with an empty key cell would
// share the same key and collapse together — silent data loss on ragged dumps.
func dedupeKey(rec []string, keyColumn int) string {
	if keyColumn >= 0 {
		if keyColumn < len(rec) {
			return "k\x00" + rec[keyColumn]
		}
		return "r\x00" + strings.Join(rec, "\x00")
	}
	return "w\x00" + strings.Join(rec, "\x00")
}

// DedupeOptions drops duplicate rows, by the whole row or by a key column.
type DedupeOptions struct {
	Delimiter rune
	HasHeader bool
	KeyColumn int // <0 = dedupe by whole row
	Progress  func(records int64)
}

// DedupeRowsFile keeps the first occurrence of each row/key. The seen-set is
// keyed on the exact key/row bytes (not a lossy hash), so distinct rows are
// never collapsed by a collision; memory grows with the number of distinct keys.
func DedupeRowsFile(ctx context.Context, srcPath, dstPath string, opts DedupeOptions) (TransformSummary, error) {
	t, err := openRowTransform(srcPath, dstPath, opts.Delimiter)
	if err != nil {
		return TransformSummary{}, err
	}
	defer t.close()
	sum := TransformSummary{Delimiter: opts.Delimiter}
	seen := map[string]struct{}{}
	first := true
	for {
		if err := contextErr(ctx); err != nil {
			return sum, err
		}
		rec, err := t.reader.Read()
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
			if err := t.writer.Write(rec); err != nil {
				return sum, err
			}
			sum.RecordsWritten++
			continue
		}
		first = false
		k := dedupeKey(rec, opts.KeyColumn)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		if err := t.writer.Write(rec); err != nil {
			return sum, err
		}
		sum.RecordsWritten++
	}
	if err := t.finish(); err != nil {
		return sum, err
	}
	return sum, nil
}

// SampleOptions keeps every Nth data row.
type SampleOptions struct {
	Delimiter rune
	HasHeader bool
	EveryN    int
	Progress  func(records int64)
}

// SampleRowsFile keeps every Nth data row (header preserved).
func SampleRowsFile(ctx context.Context, srcPath, dstPath string, opts SampleOptions) (TransformSummary, error) {
	if opts.EveryN <= 0 {
		opts.EveryN = 10
	}
	t, err := openRowTransform(srcPath, dstPath, opts.Delimiter)
	if err != nil {
		return TransformSummary{}, err
	}
	defer t.close()
	sum := TransformSummary{Delimiter: opts.Delimiter}
	first := true
	var dataIdx int64
	for {
		if err := contextErr(ctx); err != nil {
			return sum, err
		}
		rec, err := t.reader.Read()
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
			if err := t.writer.Write(rec); err != nil {
				return sum, err
			}
			sum.RecordsWritten++
			continue
		}
		first = false
		if dataIdx%int64(opts.EveryN) == 0 {
			if err := t.writer.Write(rec); err != nil {
				return sum, err
			}
			sum.RecordsWritten++
		}
		dataIdx++
	}
	if err := t.finish(); err != nil {
		return sum, err
	}
	return sum, nil
}
