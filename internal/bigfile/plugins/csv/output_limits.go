package csv

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// validateCSVOutputRecordSize applies the same field-quoting rules as
// encoding/csv.Writer without first materializing the encoded record.
func validateCSVOutputRecordSize(label string, fields []string, delimiter rune) error {
	size := int64(1) // terminating newline
	for i, field := range fields {
		if i > 0 {
			if !addCSVOutputBytes(&size, int64(utf8.RuneLen(delimiter))) {
				return csvOutputRecordLimitError(label)
			}
		}
		if !addCSVOutputBytes(&size, csvEncodedFieldSize(field, delimiter)) {
			return csvOutputRecordLimitError(label)
		}
	}
	return nil
}

func validateRepeatedCSVOutputField(label, field string, count int, delimiter rune) error {
	size := int64(1) // terminating newline
	fieldSize := csvEncodedFieldSize(field, delimiter)
	delimiterSize := int64(utf8.RuneLen(delimiter))
	for i := 0; i < count; i++ {
		if i > 0 && !addCSVOutputBytes(&size, delimiterSize) {
			return csvOutputRecordLimitError(label)
		}
		if !addCSVOutputBytes(&size, fieldSize) {
			return csvOutputRecordLimitError(label)
		}
	}
	return nil
}

func addCSVOutputBytes(total *int64, amount int64) bool {
	if amount < 0 || *total > MaxLogicalRecordBytes-amount {
		return false
	}
	*total += amount
	return true
}

func csvOutputRecordLimitError(label string) error {
	return fmt.Errorf("%s exceeds the %d-byte encoded output-record limit", label, MaxLogicalRecordBytes)
}

func csvEncodedFieldSize(field string, delimiter rune) int64 {
	size := int64(len(field))
	if !csvFieldNeedsQuotes(field, delimiter) {
		return size
	}
	return size + 2 + int64(strings.Count(field, `"`))
}

func csvFieldNeedsQuotes(field string, delimiter rune) bool {
	if field == "" {
		return false
	}
	if field == `\.` {
		return true
	}
	if strings.ContainsRune(field, delimiter) || strings.ContainsAny(field, "\"\r\n") {
		return true
	}
	first, _ := utf8.DecodeRuneInString(field)
	return unicode.IsSpace(first)
}
