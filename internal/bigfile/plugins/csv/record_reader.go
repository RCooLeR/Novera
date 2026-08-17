package csv

import (
	"bufio"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"
)

// MaxLogicalRecordBytes is the application hard ceiling for raw bytes in one
// CSV logical record, including embedded newlines and the terminating newline.
// Callers may request a smaller limit but cannot raise this ceiling.
const MaxLogicalRecordBytes int64 = 16 * 1024 * 1024

var (
	ErrCSVRecordTooLarge = errors.New("CSV logical record exceeds the configured byte limit")
	ErrCSVTooManyFields  = errors.New("CSV logical record exceeds the application field limit")
)

// RecordLimitError identifies the record and parser-input offset at which
// bounded parsing stopped. StartOffset is relative to the CSV bytes supplied
// after any BOM removal. ObservedBytes is the first size above LimitBytes.
type RecordLimitError struct {
	Record        int64
	StartOffset   int64
	LimitBytes    int64
	ObservedBytes int64
}

func (e *RecordLimitError) Error() string {
	return fmt.Sprintf("CSV record %d at byte offset %d exceeds the %d-byte logical-record limit (reached %d bytes)", e.Record, e.StartOffset, e.LimitBytes, e.ObservedBytes)
}

func (e *RecordLimitError) Unwrap() error { return ErrCSVRecordTooLarge }

// FieldLimitError identifies the logical record at which parsing stopped
// before encoding/csv could allocate a field slice wider than the shared hard
// ceiling. StartOffset is relative to the CSV bytes supplied after BOM removal.
type FieldLimitError struct {
	Record         int64
	StartOffset    int64
	LimitFields    int
	ObservedFields int
}

func (e *FieldLimitError) Error() string {
	return fmt.Sprintf("CSV record %d at byte offset %d exceeds the %d-field limit (reached %d fields)", e.Record, e.StartOffset, e.LimitFields, e.ObservedFields)
}

func (e *FieldLimitError) Unwrap() error { return ErrCSVTooManyFields }

func isCSVHardLimitError(err error) bool {
	return errors.Is(err, ErrCSVRecordTooLarge) || errors.Is(err, ErrCSVTooManyFields)
}

type csvReaderConfig struct {
	Delimiter        rune
	MaxRecordBytes   int64
	FieldsPerRecord  int
	LazyQuotes       bool
	TrimLeadingSpace bool
	ReuseRecord      bool
}

func newBoundedCSVReader(ctx context.Context, source io.Reader, config csvReaderConfig) (*stdcsv.Reader, error) {
	reader, _, err := newBoundedCSVReaderWithState(ctx, source, config)
	return reader, err
}

func newBoundedCSVReaderWithState(ctx context.Context, source io.Reader, config csvReaderConfig) (*stdcsv.Reader, *logicalRecordReader, error) {
	if source == nil {
		return nil, nil, errors.New("CSV reader source is required")
	}
	if err := ValidateDelimiter(config.Delimiter); err != nil {
		return nil, nil, err
	}
	limit, err := normalizeLogicalRecordLimit(config.MaxRecordBytes)
	if err != nil {
		return nil, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bounded := newLogicalRecordReader(ctx, source, config.Delimiter, limit, config.LazyQuotes, config.TrimLeadingSpace)
	reader := stdcsv.NewReader(bounded)
	reader.Comma = config.Delimiter
	reader.FieldsPerRecord = config.FieldsPerRecord
	reader.LazyQuotes = config.LazyQuotes
	reader.TrimLeadingSpace = config.TrimLeadingSpace
	reader.ReuseRecord = config.ReuseRecord
	return reader, bounded, nil
}

func normalizeLogicalRecordLimit(requested int64) (int64, error) {
	if requested < 0 {
		return 0, fmt.Errorf("CSV logical-record byte limit %d is negative", requested)
	}
	if requested == 0 {
		return MaxLogicalRecordBytes, nil
	}
	if requested > MaxLogicalRecordBytes {
		return 0, fmt.Errorf("CSV logical-record byte limit %d exceeds application maximum %d", requested, MaxLogicalRecordBytes)
	}
	return requested, nil
}

// logicalRecordReader mirrors encoding/csv's quote-state boundaries while
// yielding at most one logical record per Read call. It never retains a record;
// encoding/csv may buffer up to the validated cap, then receives a typed error.
type logicalRecordReader struct {
	ctx              context.Context
	source           *bufio.Reader
	delimiter        rune
	limit            int64
	lazyQuotes       bool
	trimLeadingSpace bool

	record        int64
	recordStart   int64
	recordBytes   int64
	fieldCount    int
	absoluteBytes int64
	contextBytes  int64

	fieldStart   bool
	inQuotes     bool
	afterQuote   bool
	afterQuoteCR bool

	pending         []byte
	boundaryPending bool
	pendingErr      error
}

func newLogicalRecordReader(ctx context.Context, source io.Reader, delimiter rune, limit int64, lazyQuotes, trimLeadingSpace bool) *logicalRecordReader {
	buffered, ok := source.(*bufio.Reader)
	if !ok {
		buffered = bufio.NewReaderSize(source, 32*1024)
	}
	return &logicalRecordReader{
		ctx: ctx, source: buffered, delimiter: delimiter, limit: limit,
		lazyQuotes: lazyQuotes, trimLeadingSpace: trimLeadingSpace,
		record: 1, fieldCount: 1, fieldStart: true,
	}
}

func (r *logicalRecordReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n := 0
	if len(r.pending) > 0 {
		copied := copy(p, r.pending)
		n += copied
		r.pending = r.pending[copied:]
		if len(r.pending) > 0 {
			return n, nil
		}
		if r.boundaryPending {
			r.boundaryPending = false
			return n, nil
		}
	}
	if r.pendingErr != nil {
		return n, r.pendingErr
	}

	for n < len(p) {
		if r.contextBytes >= 32*1024 {
			if err := r.ctx.Err(); err != nil {
				r.pendingErr = err
				if n > 0 {
					return n, nil
				}
				return 0, err
			}
			r.contextBytes = 0
		}

		var raw [utf8.UTFMax]byte
		rn, size, rawBytes, err := r.readRawRune(&raw)
		if err != nil {
			r.pendingErr = err
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		r.absoluteBytes += int64(size)
		r.contextBytes += int64(size)
		observed := r.recordBytes + int64(size)
		if observed > r.limit {
			r.pendingErr = &RecordLimitError{
				Record: r.record, StartOffset: r.recordStart,
				LimitBytes: r.limit, ObservedBytes: observed,
			}
			if n > 0 {
				return n, nil
			}
			return 0, r.pendingErr
		}
		r.recordBytes = observed
		boundary, nextField := r.consumeRune(rn)
		if nextField {
			r.fieldCount++
			if r.fieldCount > MaxCSVFieldsPerRecord {
				r.pendingErr = &FieldLimitError{
					Record: r.record, StartOffset: r.recordStart,
					LimitFields: MaxCSVFieldsPerRecord, ObservedFields: r.fieldCount,
				}
				if n > 0 {
					return n, nil
				}
				return 0, r.pendingErr
			}
		}

		copied := copy(p[n:], raw[:rawBytes])
		n += copied
		if copied < rawBytes {
			r.pending = append(r.pending[:0], raw[copied:rawBytes]...)
			r.boundaryPending = boundary
			if boundary {
				r.resetRecord()
			}
			return n, nil
		}
		if boundary {
			r.resetRecord()
			return n, nil
		}
	}
	return n, nil
}

func (r *logicalRecordReader) readRawRune(raw *[utf8.UTFMax]byte) (rune, int, int, error) {
	rn, size, err := r.source.ReadRune()
	if err != nil {
		return 0, 0, 0, err
	}
	if rn == utf8.RuneError && size == 1 {
		if err := r.source.UnreadRune(); err != nil {
			return 0, 0, 0, err
		}
		b, err := r.source.ReadByte()
		if err != nil {
			return 0, 0, 0, err
		}
		raw[0] = b
		return rn, 1, 1, nil
	}
	n := utf8.EncodeRune(raw[:], rn)
	return rn, size, n, nil
}

func (r *logicalRecordReader) consumeRune(current rune) (recordBoundary, fieldBoundary bool) {
	if r.afterQuoteCR {
		r.afterQuoteCR = false
		if current == '\n' {
			r.inQuotes = false
			r.afterQuote = false
			return true, false
		}
		if r.lazyQuotes {
			r.afterQuote = false
			// The bare quote and CR remain inside the quoted field. Process the
			// current rune with ordinary quoted-field rules below.
		} else {
			r.inQuotes = false
			r.afterQuote = false
		}
	}

	if r.inQuotes {
		if r.afterQuote {
			switch current {
			case '"':
				r.afterQuote = false
				return false, false
			case r.delimiter:
				r.inQuotes = false
				r.afterQuote = false
				r.fieldStart = true
				return false, true
			case '\n':
				r.inQuotes = false
				r.afterQuote = false
				return true, false
			case '\r':
				r.afterQuoteCR = true
				return false, false
			default:
				r.afterQuote = false
				if !r.lazyQuotes {
					r.inQuotes = false
					r.fieldStart = current == r.delimiter
					return false, false
				}
			}
		}
		if current == '"' {
			r.afterQuote = true
		}
		return false, false
	}

	if current == '\n' {
		return true, false
	}
	if r.fieldStart {
		if current == r.delimiter {
			return false, true
		}
		if r.trimLeadingSpace && unicode.IsSpace(current) {
			return false, false
		}
		if current == '"' {
			r.inQuotes = true
			r.afterQuote = false
		} else {
			r.fieldStart = false
		}
		return false, false
	}
	if current == r.delimiter {
		r.fieldStart = true
		return false, true
	}
	return false, false
}

func (r *logicalRecordReader) resetRecord() {
	r.record++
	r.recordStart = r.absoluteBytes
	r.recordBytes = 0
	r.fieldCount = 1
	r.fieldStart = true
	r.inQuotes = false
	r.afterQuote = false
	r.afterQuoteCR = false
}
