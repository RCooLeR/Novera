package regexutil

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"novera/internal/bigfile/units"
)

// Analysis summarizes regex compatibility checks and soft warnings.
type Analysis struct {
	Warnings []string
}

// Analyze validates a regex pattern against Quarry's current bounded-regex rules.
func Analyze(pattern []byte, caseInsensitive bool, maxMatchWindow int) (Analysis, error) {
	if len(pattern) == 0 {
		return Analysis{}, errors.New("empty pattern")
	}
	expr := string(pattern)

	if strings.Contains(expr, "(?<=") || strings.Contains(expr, "(?<!") {
		return Analysis{}, errors.New("lookbehind is not supported by Go regular expressions")
	}
	if strings.Contains(expr, `\k<`) || strings.Contains(expr, `\g<`) {
		return Analysis{}, errors.New("backreferences are not supported by Go regular expressions")
	}
	if hasNumericBackreference(expr) {
		return Analysis{}, errors.New("backreferences such as \\1 are not supported by Go regular expressions")
	}

	if _, err := Compile(pattern, caseInsensitive); err != nil {
		return Analysis{}, err
	}

	var warnings []string
	if maxMatchWindow > 0 && maybeBroadWildcard(expr) {
		warnings = append(warnings, fmt.Sprintf("Pattern contains broad wildcard quantifiers and may exceed the current %s regex window.", units.FormatBytes(int64(maxMatchWindow))))
	}
	if strings.Contains(expr, `\b`) {
		warnings = append(warnings, `Pattern uses \b word boundaries; results depend on Go regexp's ASCII-style word-character rules.`)
	}
	if hasChunkBoundaryAnchor(expr) {
		warnings = append(warnings, "Pattern uses anchors (^, $, \\A, or \\z); bounded chunked scans evaluate them against the loaded regex window, not always the whole file.")
	}

	return Analysis{Warnings: warnings}, nil
}

// Compile validates and compiles a regex pattern under Quarry's bounded-regex
// policy. Patterns that can match empty text are refused because they do not
// consume input and can make search/replace counts ambiguous across chunks.
func Compile(pattern []byte, caseInsensitive bool) (*regexp.Regexp, error) {
	if len(pattern) == 0 {
		return nil, errors.New("empty pattern")
	}
	expr := string(pattern)
	compiledExpr := expr
	if caseInsensitive {
		compiledExpr = "(?i)" + compiledExpr
	}
	re, err := regexp.Compile(compiledExpr)
	if err != nil {
		return nil, err
	}
	if regexMatchesEmptyText(re) {
		return nil, errors.New("regex must not match empty text")
	}
	return re, nil
}

func regexMatchesEmptyText(re *regexp.Regexp) bool {
	if match := re.FindIndex([]byte("")); match != nil && match[0] == match[1] {
		return true
	}
	if match := re.FindIndex([]byte("x")); match != nil && match[0] == match[1] {
		return true
	}
	return false
}

func hasNumericBackreference(expr string) bool {
	escaped := false
	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		if escaped {
			escaped = false
			if ch >= '1' && ch <= '9' {
				return true
			}
			continue
		}
		if ch == '\\' {
			escaped = true
		}
	}
	return false
}

func maybeBroadWildcard(expr string) bool {
	candidates := []string{
		".*",
		".+",
		".*?",
		".+?",
		"[^\\n]*",
		"[^\\n]+",
	}
	for _, candidate := range candidates {
		if strings.Contains(expr, candidate) {
			return true
		}
	}
	return false
}

func hasChunkBoundaryAnchor(expr string) bool {
	escaped := false
	inClass := false
	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		if escaped {
			escaped = false
			if ch == 'A' || ch == 'z' {
				return true
			}
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '[' {
			inClass = true
			continue
		}
		if ch == ']' {
			inClass = false
			continue
		}
		if !inClass && (ch == '^' || ch == '$') {
			return true
		}
	}
	return false
}
