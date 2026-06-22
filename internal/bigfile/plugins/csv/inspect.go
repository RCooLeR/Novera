package csv

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	DefaultInspectMaxBytes = int64(4 * 1024 * 1024)
	DefaultInspectMaxRows  = 100
)

type InspectOptions struct {
	MaxBytes   int64
	MaxRows    int
	Delimiters []rune
}

type DelimiterCandidate struct {
	Delimiter      rune
	Name           string
	RecordsParsed  int
	RowsWithFields int
	Columns        int
	ConsistentRows int
	AverageColumns float64
	Score          float64
	ParseError     string
}

type DelimiterReport struct {
	Delimiter       rune
	DelimiterName   string
	Confidence      string
	Columns         int
	RecordsScanned  int
	BytesScanned    int64
	TruncatedSample bool
	HasHeader       bool
	Candidates      []DelimiterCandidate
	Warnings        []string
}

func InspectReader(r io.Reader, opts InspectOptions) (DelimiterReport, error) {
	return InspectReaderContext(context.Background(), r, opts)
}

func InspectReaderContext(ctx context.Context, r io.Reader, opts InspectOptions) (DelimiterReport, error) {
	if r == nil {
		return DelimiterReport{}, errors.New("reader is required")
	}
	opts = normalizeInspectOptions(opts)
	data, err := readBoundedSample(ctx, r, opts.MaxBytes)
	if err != nil {
		return DelimiterReport{}, err
	}

	truncated := int64(len(data)) > opts.MaxBytes
	if truncated {
		data = data[:opts.MaxBytes]
	}

	report := DelimiterReport{
		BytesScanned:    int64(len(data)),
		TruncatedSample: truncated,
	}
	if len(bytes.TrimSpace(data)) == 0 {
		report.Confidence = "none"
		report.Warnings = append(report.Warnings, "sample is empty")
		return report, nil
	}
	if truncated {
		report.Warnings = append(report.Warnings, fmt.Sprintf("sample limited to %d bytes", opts.MaxBytes))
	}

	candidates := make([]DelimiterCandidate, 0, len(opts.Delimiters))
	for _, delimiter := range opts.Delimiters {
		if err := contextErr(ctx); err != nil {
			return report, err
		}
		candidates = append(candidates, inspectDelimiter(data, delimiter, opts.MaxRows))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Name < candidates[j].Name
	})
	report.Candidates = candidates
	if len(candidates) == 0 || candidates[0].Score <= 0 || candidates[0].Columns <= 1 {
		report.Confidence = "none"
		report.Warnings = append(report.Warnings, "no consistent delimiter detected")
		return report, nil
	}

	best := candidates[0]
	report.Delimiter = best.Delimiter
	report.DelimiterName = best.Name
	report.Columns = best.Columns
	report.RecordsScanned = best.RecordsParsed
	report.HasHeader = likelyHeader(data, best.Delimiter, opts.MaxRows)
	report.Confidence = delimiterConfidence(best, candidates)
	return report, nil
}

func normalizeInspectOptions(opts InspectOptions) InspectOptions {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultInspectMaxBytes
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = DefaultInspectMaxRows
	}
	if len(opts.Delimiters) == 0 {
		opts.Delimiters = []rune{',', '\t', ';', '|'}
	}
	return opts
}

func inspectDelimiter(data []byte, delimiter rune, maxRows int) DelimiterCandidate {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	reader.ReuseRecord = false

	lengths := make(map[int]int)
	totalColumns := 0
	candidate := DelimiterCandidate{
		Delimiter: delimiter,
		Name:      delimiterName(delimiter),
	}
	for candidate.RecordsParsed < maxRows {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			candidate.ParseError = err.Error()
			break
		}
		candidate.RecordsParsed++
		fields := len(record)
		if fields > 1 {
			candidate.RowsWithFields++
			lengths[fields]++
			totalColumns += fields
		}
	}
	if candidate.RowsWithFields == 0 {
		return candidate
	}

	modalColumns, consistentRows := modalFieldCount(lengths)
	candidate.Columns = modalColumns
	candidate.ConsistentRows = consistentRows
	candidate.AverageColumns = float64(totalColumns) / float64(candidate.RowsWithFields)
	consistency := float64(consistentRows) / float64(candidate.RowsWithFields)
	fieldStrength := math.Min(candidate.AverageColumns, 64) / 64
	rowStrength := math.Min(float64(candidate.RowsWithFields), 25) / 25
	candidate.Score = consistency*70 + fieldStrength*20 + rowStrength*10
	if candidate.ParseError != "" {
		candidate.Score *= 0.75
	}
	return candidate
}

func modalFieldCount(lengths map[int]int) (int, int) {
	bestFields := 0
	bestRows := 0
	for fields, rows := range lengths {
		if rows > bestRows || (rows == bestRows && fields > bestFields) {
			bestFields = fields
			bestRows = rows
		}
	}
	return bestFields, bestRows
}

func delimiterConfidence(best DelimiterCandidate, candidates []DelimiterCandidate) string {
	if best.ConsistentRows == best.RowsWithFields && best.RowsWithFields >= 3 {
		if len(candidates) < 2 || best.Score-candidates[1].Score >= 15 {
			return "high"
		}
	}
	if best.RowsWithFields >= 2 && best.Score >= 50 {
		return "medium"
	}
	return "low"
}

func likelyHeader(data []byte, delimiter rune, maxRows int) bool {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	first, err := readNonEmptyRecord(reader, maxRows)
	if err != nil || len(first) == 0 {
		return false
	}
	second, err := readNonEmptyRecord(reader, maxRows)
	if err != nil || len(second) == 0 || len(first) != len(second) {
		return false
	}

	textyFirst := 0
	dataLikeSecond := 0
	for i := range first {
		if looksTextualHeader(first[i]) {
			textyFirst++
		}
		if looksDataValue(second[i]) {
			dataLikeSecond++
		}
	}
	return textyFirst > 0 && dataLikeSecond >= len(second)/2
}

func readNonEmptyRecord(reader *csv.Reader, maxRows int) ([]string, error) {
	for i := 0; i < maxRows; i++ {
		record, err := reader.Read()
		if err != nil {
			return nil, err
		}
		for _, field := range record {
			if strings.TrimSpace(field) != "" {
				return record, nil
			}
		}
	}
	return nil, io.EOF
}

func looksTextualHeader(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || looksNumber(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func looksDataValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if looksNumber(value) {
		return true
	}
	lower := strings.ToLower(value)
	return lower == "true" || lower == "false" || lower == "null" || lower == "nil"
}

func looksNumber(value string) bool {
	if value == "" {
		return false
	}
	digit := false
	for i, r := range value {
		switch {
		case unicode.IsDigit(r):
			digit = true
		case r == '.' || r == ',' || r == '_':
		case (r == '-' || r == '+') && i == 0:
		default:
			return false
		}
	}
	return digit
}

func delimiterName(delimiter rune) string {
	switch delimiter {
	case ',':
		return "comma"
	case '\t':
		return "tab"
	case ';':
		return "semicolon"
	case '|':
		return "pipe"
	default:
		return string(delimiter)
	}
}
