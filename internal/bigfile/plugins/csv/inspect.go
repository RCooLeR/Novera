package csv

import (
	"bytes"
	"context"
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
	MaxBytes       int64
	MaxRows        int
	MaxRecordBytes int64
	Delimiters     []rune
	// SourceTruncated tells delimiter evaluation to omit the final logical
	// record unless it ends at a parser-confirmed newline boundary.
	SourceTruncated bool
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
	opts, err := normalizeInspectOptions(opts)
	if err != nil {
		return DelimiterReport{}, err
	}
	sample, err := readBoundedSampleResult(ctx, r, opts.MaxBytes)
	if err != nil {
		return DelimiterReport{}, err
	}
	data := sample.Data
	sourceTruncated := sample.Truncated || opts.SourceTruncated

	report := DelimiterReport{
		BytesScanned:    sample.BytesScanned,
		TruncatedSample: sample.Truncated,
	}
	if len(bytes.TrimSpace(data)) == 0 {
		report.Confidence = "none"
		report.Warnings = append(report.Warnings, "sample is empty")
		return report, nil
	}
	if sample.Truncated {
		report.Warnings = append(report.Warnings, fmt.Sprintf("sample limited to %d bytes", opts.MaxBytes))
	}
	if sourceTruncated {
		report.TruncatedSample = true
		report.Warnings = append(report.Warnings, "trailing partial logical record omitted from delimiter inspection")
	}

	candidates := make([]DelimiterCandidate, 0, len(opts.Delimiters))
	var fieldLimitErr error
	for _, delimiter := range opts.Delimiters {
		if err := contextErr(ctx); err != nil {
			return report, err
		}
		candidateData := data
		if sourceTruncated {
			candidateData, _, err = CompleteRecordPrefix(ctx, data, delimiter, opts.MaxRecordBytes, true)
			if err != nil {
				if errors.Is(err, ErrCSVRecordTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return report, err
				}
				if errors.Is(err, ErrCSVTooManyFields) && fieldLimitErr == nil {
					fieldLimitErr = err
				}
				candidates = append(candidates, DelimiterCandidate{
					Delimiter:  delimiter,
					Name:       delimiterName(delimiter),
					ParseError: err.Error(),
				})
				continue
			}
		}
		candidate, err := inspectDelimiter(ctx, candidateData, delimiter, opts.MaxRows, opts.MaxRecordBytes)
		if err != nil {
			if !errors.Is(err, ErrCSVTooManyFields) {
				return report, err
			}
			if fieldLimitErr == nil {
				fieldLimitErr = err
			}
			candidate.ParseError = err.Error()
		}
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Name < candidates[j].Name
	})
	report.Candidates = candidates
	if len(candidates) == 0 || candidates[0].Score <= 0 || candidates[0].Columns <= 1 {
		if fieldLimitErr != nil {
			return report, fieldLimitErr
		}
		report.Confidence = "none"
		report.Warnings = append(report.Warnings, "no consistent delimiter detected")
		return report, nil
	}

	best := candidates[0]
	report.Delimiter = best.Delimiter
	report.DelimiterName = best.Name
	report.Columns = best.Columns
	report.RecordsScanned = best.RecordsParsed
	headerData := data
	if sourceTruncated {
		headerData, _, err = CompleteRecordPrefix(ctx, data, best.Delimiter, opts.MaxRecordBytes, true)
		if err != nil {
			return report, err
		}
	}
	report.HasHeader, err = likelyHeader(ctx, headerData, best.Delimiter, opts.MaxRows, opts.MaxRecordBytes)
	if err != nil {
		return report, err
	}
	report.Confidence = delimiterConfidence(best, candidates)
	return report, nil
}

func normalizeInspectOptions(opts InspectOptions) (InspectOptions, error) {
	maxBytes, err := normalizeSampleByteLimit("inspect sample", opts.MaxBytes, DefaultInspectMaxBytes)
	if err != nil {
		return opts, err
	}
	maxRows, err := normalizeSampleRowLimit("inspect sample", opts.MaxRows, DefaultInspectMaxRows)
	if err != nil {
		return opts, err
	}
	opts.MaxBytes = maxBytes
	opts.MaxRows = maxRows
	if len(opts.Delimiters) == 0 {
		opts.Delimiters = []rune{',', '\t', ';', '|'}
	}
	for _, delimiter := range opts.Delimiters {
		if err := ValidateDelimiter(delimiter); err != nil {
			return opts, err
		}
	}
	limit, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes)
	if err != nil {
		return opts, err
	}
	opts.MaxRecordBytes = limit
	return opts, nil
}

func inspectDelimiter(ctx context.Context, data []byte, delimiter rune, maxRows int, maxRecordBytes int64) (DelimiterCandidate, error) {
	reader, err := newBoundedCSVReader(ctx, bytes.NewReader(data), csvReaderConfig{
		Delimiter: delimiter, MaxRecordBytes: maxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false, TrimLeadingSpace: true,
	})
	if err != nil {
		return DelimiterCandidate{}, err
	}

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
			if errors.Is(err, ErrCSVRecordTooLarge) || errors.Is(err, ErrCSVTooManyFields) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return candidate, err
			}
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
		return candidate, nil
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
	return candidate, nil
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

func likelyHeader(ctx context.Context, data []byte, delimiter rune, maxRows int, maxRecordBytes int64) (bool, error) {
	reader, err := newBoundedCSVReader(ctx, bytes.NewReader(data), csvReaderConfig{
		Delimiter: delimiter, MaxRecordBytes: maxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false, TrimLeadingSpace: true,
	})
	if err != nil {
		return false, err
	}

	first, err := readNonEmptyRecord(reader, maxRows)
	if err != nil {
		if isCSVHardLimitError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, nil
	}
	if len(first) == 0 {
		return false, nil
	}
	second, err := readNonEmptyRecord(reader, maxRows)
	if err != nil {
		if isCSVHardLimitError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, nil
	}
	if len(second) == 0 || len(first) != len(second) {
		return false, nil
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
	return textyFirst > 0 && dataLikeSecond >= len(second)/2, nil
}

func readNonEmptyRecord(reader interface{ Read() ([]string, error) }, maxRows int) ([]string, error) {
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
