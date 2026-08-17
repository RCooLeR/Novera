package csv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/regularfile"
)

// JSONLOptions configures a CSV → JSON Lines export.
type JSONLOptions struct {
	Delimiter      rune
	HasHeader      bool
	NumberKeys     bool // emit numeric-looking cells as JSON numbers, not strings
	MaxRecordBytes int64
	Progress       func(records int64)
}

// ExportSummary reports a tabular export result.
type ExportSummary struct {
	RecordsRead    int64
	RecordsWritten int64
}

// ExportJSONLFile streams a CSV to newline-delimited JSON (one object per data
// row). Keys come from the header row, or col1, col2, … when there is none.
func normalizeJSONLOptions(opts JSONLOptions) (JSONLOptions, error) {
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if err := ValidateDelimiter(opts.Delimiter); err != nil {
		return opts, err
	}
	limit, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes)
	if err != nil {
		return opts, err
	}
	opts.MaxRecordBytes = limit
	return opts, nil
}

// ValidateJSONLOptions validates export configuration without opening a source
// or destination.
func ValidateJSONLOptions(opts JSONLOptions) error {
	_, err := normalizeJSONLOptions(opts)
	return err
}

// ExportJSONL streams records between caller-owned streams. File publication
// and source-generation validation remain the caller's responsibility.
func ExportJSONL(ctx context.Context, r io.Reader, w io.Writer, opts JSONLOptions) (ExportSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return ExportSummary{}, errors.New("reader is required")
	}
	if w == nil {
		return ExportSummary{}, errors.New("writer is required")
	}
	normalized, err := normalizeJSONLOptions(opts)
	if err != nil {
		return ExportSummary{}, err
	}
	opts = normalized

	br := bufio.NewReader(r)
	if err := skipInputBOM(br); err != nil {
		return ExportSummary{}, err
	}
	reader, err := newBoundedCSVReader(ctx, br, csvReaderConfig{
		Delimiter: opts.Delimiter, MaxRecordBytes: opts.MaxRecordBytes,
		FieldsPerRecord: 0, LazyQuotes: false,
	})
	if err != nil {
		return ExportSummary{}, err
	}

	var sum ExportSummary
	var firstData []string
	var keys []string
	first, readErr := reader.Read()
	if errors.Is(readErr, io.EOF) {
		if opts.HasHeader {
			return sum, errors.New("JSONL export requires the configured header row")
		}
	} else if readErr != nil {
		return sum, readErr
	} else {
		sum.RecordsRead++
		if opts.HasHeader {
			keys, err = jsonlColumnKeys(first)
		} else {
			keys, err = jsonlColumnKeys(make([]string, len(first)))
			if err == nil {
				err = validateJSONLRecord(first, sum.RecordsRead)
			}
			firstData = first
		}
		if err != nil {
			return sum, err
		}
	}

	bw := bufio.NewWriterSize(w, 1<<20)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	writeRecord := func(rec []string) error {
		if err := validateJSONLRecord(rec, sum.RecordsRead); err != nil {
			return err
		}
		obj := make(map[string]any, len(rec))
		for i, cell := range rec {
			if opts.NumberKeys {
				obj[keys[i]] = maybeNumber(cell)
			} else {
				obj[keys[i]] = cell
			}
		}
		if err := enc.Encode(obj); err != nil {
			return err
		}
		sum.RecordsWritten++
		return nil
	}
	if firstData != nil {
		if err := writeRecord(firstData); err != nil {
			return sum, err
		}
	}
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
		if err := writeRecord(rec); err != nil {
			return sum, err
		}
	}
	if err := bw.Flush(); err != nil {
		return sum, err
	}
	return sum, contextErr(ctx)
}

// ExportJSONLFile publishes only a complete output and refuses existing
// destinations and aliases of the source.
func ExportJSONLFile(ctx context.Context, srcPath, dstPath string, opts JSONLOptions) (_ ExportSummary, retErr error) {
	normalized, err := normalizeJSONLOptions(opts)
	if err != nil {
		return ExportSummary{}, err
	}
	opts = normalized
	input, err := regularfile.Open(srcPath)
	if err != nil {
		return ExportSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, input.Close()) }()
	output, err := fileio.OpenAtomicOutput(dstPath, []string{srcPath}, 0o600)
	if err != nil {
		return ExportSummary{}, err
	}
	defer func() { retErr = errors.Join(retErr, output.Cleanup()) }()

	summary, err := ExportJSONL(ctx, input, output, opts)
	if err != nil {
		return summary, err
	}
	if err := output.CommitContext(ctx); err != nil {
		return summary, err
	}
	return summary, nil
}

func jsonlColumnKeys(header []string) ([]string, error) {
	keys := make([]string, len(header))
	seen := make(map[string]int, len(header))
	for i := range header {
		key := header[i]
		if !utf8.ValidString(key) {
			return nil, fmt.Errorf("JSONL header column %d is not valid UTF-8", i+1)
		}
		if strings.TrimSpace(key) == "" {
			key = fmt.Sprintf("col%d", i+1)
		}
		if previous, exists := seen[key]; exists {
			return nil, fmt.Errorf("JSONL object key %q is derived by both CSV columns %d and %d", key, previous+1, i+1)
		}
		seen[key] = i
		keys[i] = key
	}
	return keys, nil
}

func validateJSONLRecord(record []string, recordNumber int64) error {
	for column, cell := range record {
		if !utf8.ValidString(cell) {
			return fmt.Errorf("CSV record %d column %d is not valid UTF-8 for JSONL export", recordNumber, column+1)
		}
	}
	return nil
}

// maybeNumber emits only exact JSON-number tokens. json.Number preserves every
// source digit and exponent byte without a float64 round trip. Tokens with a
// leading plus, signed/unsigned leading zeros, whitespace, NaN, or Inf remain
// strings because converting them would change their identity or JSON spelling.
func maybeNumber(s string) any {
	if isExactJSONNumber(s) {
		return json.Number(s)
	}
	return s
}

func isExactJSONNumber(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	i := 0
	if value[i] == '-' {
		i++
		if i == len(value) {
			return false
		}
	}
	if value[i] == '0' {
		i++
		if i < len(value) && value[i] >= '0' && value[i] <= '9' {
			return false
		}
	} else {
		if value[i] < '1' || value[i] > '9' {
			return false
		}
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
	}
	if i < len(value) && value[i] == '.' {
		i++
		start := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
		i++
		if i < len(value) && (value[i] == '+' || value[i] == '-') {
			i++
		}
		start := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == len(value)
}

// MarkdownPreview renders a small set of rows as a GitHub-flavored Markdown
// table. Used for "copy as Markdown" of the current preview (bounded).
func MarkdownPreview(header []string, rows [][]string) string {
	cols := len(header)
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return ""
	}
	var b strings.Builder
	cell := func(r []string, i int) string {
		v := ""
		if i < len(r) {
			v = r[i]
		}
		return strings.ReplaceAll(strings.ReplaceAll(v, "|", "\\|"), "\n", " ")
	}
	// header
	b.WriteByte('|')
	for i := 0; i < cols; i++ {
		h := fmt.Sprintf("col%d", i+1)
		if i < len(header) && strings.TrimSpace(header[i]) != "" {
			h = header[i]
		}
		b.WriteByte(' ')
		b.WriteString(strings.ReplaceAll(h, "|", "\\|"))
		b.WriteString(" |")
	}
	b.WriteByte('\n')
	// separator
	b.WriteByte('|')
	for i := 0; i < cols; i++ {
		b.WriteString(" --- |")
	}
	b.WriteByte('\n')
	// rows
	for _, r := range rows {
		b.WriteByte('|')
		for i := 0; i < cols; i++ {
			b.WriteByte(' ')
			b.WriteString(cell(r, i))
			b.WriteString(" |")
		}
		b.WriteByte('\n')
	}
	return b.String()
}
