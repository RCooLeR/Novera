package bigfile

import (
	"fmt"
	"strings"

	"novera/internal/bigfile/regexutil"
	"novera/internal/bigfile/search"
)

// validateServiceSearchQuery rejects caller-controlled bridge strings before
// converting or encoding them into another byte slice. Lower-level packages
// enforce the same limits independently.
func validateServiceSearchQuery(query string, regex bool) error {
	if regex {
		if len(query) > regexutil.MaxPatternBytes {
			return fmt.Errorf("%w: pattern has %d bytes, maximum is %d", regexutil.ErrRegexResourceLimit, len(query), regexutil.MaxPatternBytes)
		}
		return nil
	}
	if len(query) > search.MaxPlainPatternBytes {
		return fmt.Errorf("%w: plain pattern has %d bytes, maximum is %d", search.ErrResourceLimit, len(query), search.MaxPlainPatternBytes)
	}
	return nil
}

func plainSearchUnsupportedReason(encoding string, query string, caseSensitive bool, wholeWord bool) string {
	encoding = strings.TrimSpace(encoding)
	isUTF8 := encoding == "" || strings.EqualFold(encoding, "UTF-8")
	if wholeWord && !isUTF8 {
		return "Whole-word search is unavailable for " + encoding + " files because byte boundaries do not represent character boundaries. Convert the file to UTF-8 or turn off Whole word."
	}
	if !caseSensitive && !isASCIIText(query) {
		return "Case-insensitive plain search currently supports ASCII terms only. Turn on Match case for this term."
	}
	return ""
}

func plainSearchByteAlignment(encoding string) int {
	if strings.EqualFold(strings.TrimSpace(encoding), "UTF-16LE") || strings.EqualFold(strings.TrimSpace(encoding), "UTF-16BE") {
		return 2
	}
	return 1
}

func isASCIIText(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}
