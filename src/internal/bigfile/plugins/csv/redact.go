package csv

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	stdcsv "encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/regularfile"
)

// RedactMode is how a column's values are masked.
type RedactMode string

const (
	RedactNull  RedactMode = "null"  // empty value
	RedactFixed RedactMode = "fixed" // a constant replacement
	RedactHash  RedactMode = "hash"  // operation-keyed, 128-bit HMAC pseudonym
	RedactEmail RedactMode = "email" // keep first char + domain: a***@x.com
)

const (
	PseudonymKeyBytes    = 32
	MaxPseudonymKeyBytes = 64
	pseudonymOutputBytes = 16
)

type RedactOptions struct {
	Delimiter      rune
	HasHeader      bool
	Columns        map[int]RedactMode // 0-based source column → mask
	Replacement    string             // used by RedactFixed (default "REDACTED")
	PseudonymKey   []byte
	MaxRecordBytes int64
	Progress       func(records int64, bytes int64)
}

type RedactSummary struct {
	RecordsRead    int64
	RecordsWritten int64
	CellsMasked    int64 // selected cells whose value changed under the mask
	Delimiter      rune
}

// ValidateRedactOptions rejects caller-controlled configurations that could
// otherwise publish selected cells unchanged while reporting a successful
// redaction.
func ValidateRedactOptions(opts RedactOptions) error {
	if err := validateTransformColumnMappingCount("CSV redaction", len(opts.Columns), true); err != nil {
		if len(opts.Columns) == 0 {
			return errors.New("select at least one column to redact")
		}
		return err
	}
	configStringBytes := 0
	if err := addTransformConfigString(&configStringBytes, "CSV redaction", opts.Replacement); err != nil {
		return err
	}
	for _, mode := range opts.Columns {
		if err := addTransformConfigString(&configStringBytes, "CSV redaction", string(mode)); err != nil {
			return err
		}
	}
	if err := ValidateDelimiter(opts.Delimiter); err != nil {
		return err
	}
	if _, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes); err != nil {
		return err
	}
	fixedColumns := 0
	for _, index := range sortedRedactColumnIndexes(opts.Columns) {
		if index < 0 {
			return fmt.Errorf("redaction column index %d is negative", index)
		}
		if !validRedactMode(opts.Columns[index]) {
			return fmt.Errorf("invalid redaction mode %q for column %d", opts.Columns[index], index)
		}
		if opts.Columns[index] == RedactFixed {
			fixedColumns++
		}
	}
	if redactUsesHash(opts.Columns) {
		if len(opts.PseudonymKey) < PseudonymKeyBytes {
			return fmt.Errorf("hash redaction requires a per-operation key of at least %d bytes", PseudonymKeyBytes)
		}
		if len(opts.PseudonymKey) > MaxPseudonymKeyBytes {
			return fmt.Errorf("hash redaction key exceeds %d-byte limit", MaxPseudonymKeyBytes)
		}
	}
	if err := validateRepeatedCSVOutputField(
		"CSV redaction fixed-value expansion",
		opts.Replacement,
		fixedColumns,
		opts.Delimiter,
	); err != nil {
		return err
	}
	return nil
}

func redactUsesHash(columns map[int]RedactMode) bool {
	for _, mode := range columns {
		if mode == RedactHash {
			return true
		}
	}
	return false
}

// NewPseudonymKey creates a fresh operation-local key. Reusing a key
// deliberately links identical values; the service creates a new key for every
// redaction export.
func NewPseudonymKey() ([]byte, error) {
	key := make([]byte, PseudonymKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate redaction pseudonym key: %w", err)
	}
	return key, nil
}

func validRedactMode(mode RedactMode) bool {
	switch mode {
	case RedactNull, RedactFixed, RedactHash, RedactEmail:
		return true
	default:
		return false
	}
}

func sortedRedactColumnIndexes(columns map[int]RedactMode) []int {
	indexes := make([]int, 0, len(columns))
	for index := range columns {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

func validateRedactColumnRange(columns map[int]RedactMode, fieldCount int) error {
	for _, index := range sortedRedactColumnIndexes(columns) {
		if index >= fieldCount {
			return fmt.Errorf("redaction column index %d is outside the first record's %d fields", index, fieldCount)
		}
	}
	return nil
}

func maskValue(value string, mode RedactMode, replacement string, pseudonymKey []byte) (string, error) {
	switch mode {
	case RedactNull:
		return "", nil
	case RedactFixed:
		return replacement, nil
	case RedactHash:
		if value == "" {
			return "", nil
		}
		if len(pseudonymKey) < PseudonymKeyBytes || len(pseudonymKey) > MaxPseudonymKeyBytes {
			return "", errors.New("hash redaction requires a valid per-operation pseudonym key")
		}
		mac := hmac.New(sha256.New, pseudonymKey)
		_, _ = mac.Write([]byte(value))
		sum := mac.Sum(nil)
		return hex.EncodeToString(sum[:pseudonymOutputBytes]), nil
	case RedactEmail:
		if !utf8.ValidString(value) {
			return "", errors.New("cannot email-mask invalid UTF-8")
		}
		at := strings.LastIndexByte(value, '@')
		if at <= 0 {
			if value == "" {
				return "", nil
			}
			_, size := utf8.DecodeRuneInString(value)
			return value[:size] + "***", nil
		}
		_, size := utf8.DecodeRuneInString(value)
		return value[:size] + "***" + value[at:], nil
	default:
		return "", fmt.Errorf("invalid redaction mode %q", mode)
	}
}

func normalizeRedactOptions(opts RedactOptions) (RedactOptions, error) {
	if len(opts.Columns) == 0 {
		return opts, errors.New("select at least one column to redact")
	}
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if opts.Replacement == "" {
		opts.Replacement = "REDACTED"
	}
	if err := ValidateRedactOptions(opts); err != nil {
		return opts, err
	}
	limit, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes)
	if err != nil {
		return opts, err
	}
	opts.MaxRecordBytes = limit
	// Own the operation key for the entire stream. A caller cannot alter
	// pseudonyms mid-output by mutating the supplied key buffer.
	opts.PseudonymKey = append([]byte(nil), opts.PseudonymKey...)
	return opts, nil
}

// RedactColumns masks selected cells between caller-owned streams. The first
// record establishes the exact width; later ragged rows fail closed.
func RedactColumns(ctx context.Context, r io.Reader, w io.Writer, opts RedactOptions) (RedactSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return RedactSummary{}, errors.New("reader is required")
	}
	if w == nil {
		return RedactSummary{}, errors.New("writer is required")
	}
	normalized, err := normalizeRedactOptions(opts)
	if err != nil {
		return RedactSummary{}, err
	}
	opts = normalized

	br := bufio.NewReader(r)
	if err := skipInputBOM(br); err != nil {
		return RedactSummary{}, err
	}
	reader, err := newBoundedCSVReader(ctx, br, csvReaderConfig{
		Delimiter: opts.Delimiter, MaxRecordBytes: opts.MaxRecordBytes,
		FieldsPerRecord: 0, LazyQuotes: false,
	})
	if err != nil {
		return RedactSummary{}, err
	}
	firstRecord, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return RedactSummary{}, errors.New("cannot redact an empty CSV")
	}
	if err != nil {
		return RedactSummary{}, err
	}
	if err := validateRedactColumnRange(opts.Columns, len(firstRecord)); err != nil {
		return RedactSummary{}, err
	}
	writer := stdcsv.NewWriter(w)
	writer.Comma = opts.Delimiter
	summary, err := redactRecords(ctx, reader, writer, firstRecord, opts)
	if err != nil {
		return summary, err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return summary, err
	}
	return summary, ctx.Err()
}

// RedactColumnsFile streams src to dst (CSV), masking the configured columns.
// The header row (if any) is passed through unchanged.
func RedactColumnsFile(ctx context.Context, srcPath, dstPath string, opts RedactOptions) (_ RedactSummary, retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeRedactOptions(opts)
	if err != nil {
		return RedactSummary{}, err
	}
	opts = normalized
	in, err := regularfile.Open(srcPath)
	if err != nil {
		return RedactSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, in.Close()) }()

	br := bufio.NewReader(in)
	if err := skipInputBOM(br); err != nil {
		return RedactSummary{}, err
	}
	reader, err := newBoundedCSVReader(ctx, br, csvReaderConfig{
		Delimiter: opts.Delimiter, MaxRecordBytes: opts.MaxRecordBytes,
		FieldsPerRecord: 0, LazyQuotes: false,
	})
	if err != nil {
		return RedactSummary{}, err
	}
	firstRecord, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return RedactSummary{}, errors.New("cannot redact an empty CSV")
	}
	if err != nil {
		return RedactSummary{}, err
	}
	if err := validateRedactColumnRange(opts.Columns, len(firstRecord)); err != nil {
		return RedactSummary{}, err
	}

	// Do not create the destination until the configuration and the first
	// record's actual shape prove that every selected column can be masked.
	out, err := fileio.OpenAtomicOutput(dstPath, []string{srcPath}, 0o600)
	if err != nil {
		return RedactSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, out.Cleanup()) }()

	bw := bufio.NewWriter(out)
	writer := stdcsv.NewWriter(bw)
	writer.Comma = opts.Delimiter

	summary, err := redactRecords(ctx, reader, writer, firstRecord, opts)
	if err != nil {
		return summary, err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return summary, err
	}
	if err := bw.Flush(); err != nil {
		return summary, err
	}
	if err := out.CommitContext(ctx); err != nil {
		return summary, err
	}
	return summary, nil
}

func redactRecords(ctx context.Context, reader *stdcsv.Reader, writer *stdcsv.Writer, firstRecord []string, opts RedactOptions) (RedactSummary, error) {
	summary := RedactSummary{Delimiter: opts.Delimiter}
	first := true
	record := firstRecord
	for {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		if record == nil {
			next, err := reader.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return summary, err
			}
			record = next
		}
		summary.RecordsRead++
		if first && opts.HasHeader {
			first = false
			if err := validateCSVOutputRecordSize("redacted CSV header record", record, opts.Delimiter); err != nil {
				return summary, fmt.Errorf("record %d: %w", summary.RecordsRead, err)
			}
			if err := writer.Write(record); err != nil {
				return summary, err
			}
			summary.RecordsWritten++
			record = nil
			continue
		}
		first = false
		for i := range record {
			if mode, ok := opts.Columns[i]; ok {
				masked, err := maskValue(record[i], mode, opts.Replacement, opts.PseudonymKey)
				if err != nil {
					return summary, fmt.Errorf("record %d column %d: %w", summary.RecordsRead, i, err)
				}
				if masked != record[i] {
					summary.CellsMasked++
				}
				record[i] = masked
			}
		}
		if err := validateCSVOutputRecordSize("redacted CSV record", record, opts.Delimiter); err != nil {
			return summary, fmt.Errorf("record %d: %w", summary.RecordsRead, err)
		}
		if err := writer.Write(record); err != nil {
			return summary, err
		}
		summary.RecordsWritten++
		if opts.Progress != nil && summary.RecordsRead%5000 == 0 {
			opts.Progress(summary.RecordsRead, 0)
		}
		record = nil
	}
	return summary, nil
}
