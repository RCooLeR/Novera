package regexutil

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Analysis summarizes regex compatibility checks and soft warnings.
type Analysis struct {
	Warnings      []string
	MaxMatchBytes int64
}

// MaxExactMatchWindowBytes is the largest overlap window accepted by Novera's
// exact chunked regex paths.
const MaxExactMatchWindowBytes = 16 * 1024 * 1024

// MaxPatternBytes bounds regexp parsing and compilation. Go's regexp compiler
// builds syntax and program structures larger than the source expression.
const MaxPatternBytes = 64 * 1024

var (
	ErrUnboundedRegex       = errors.New("regex has no finite maximum match width")
	ErrRegexExceedsWindow   = errors.New("regex maximum match width exceeds the configured window")
	ErrRegexBoundaryContext = errors.New("regex boundary assertions are unsupported in exact chunked mode")
	ErrRegexResourceLimit   = errors.New("regex resource limit exceeded")
)

// Analyze validates a regex pattern against Novera's bounded-regex rules.
func Analyze(pattern []byte, caseInsensitive bool, maxMatchWindow int) (Analysis, error) {
	if err := validatePatternSize(pattern); err != nil {
		return Analysis{}, err
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
	maxBytes, unbounded, boundary, err := maximumMatchBytes(expr, caseInsensitive)
	if err != nil {
		return Analysis{}, err
	}
	if maxMatchWindow > 0 {
		if boundary {
			return Analysis{}, ErrRegexBoundaryContext
		}
		if unbounded {
			return Analysis{}, ErrUnboundedRegex
		}
		if maxBytes > int64(maxMatchWindow) {
			return Analysis{}, fmt.Errorf("%w: %d > %d bytes", ErrRegexExceedsWindow, maxBytes, maxMatchWindow)
		}
	}

	var warnings []string
	if maxMatchWindow <= 0 && maybeBroadWildcard(expr) {
		warnings = append(warnings, "Pattern contains broad wildcard quantifiers and has no configured exact-match window.")
	}
	if strings.Contains(expr, `\b`) {
		warnings = append(warnings, `Pattern uses \b word boundaries; results depend on Go regexp's ASCII-style word-character rules.`)
	}
	if hasChunkBoundaryAnchor(expr) {
		warnings = append(warnings, "Pattern uses anchors (^, $, \\A, or \\z); bounded chunked scans evaluate them against the loaded regex window, not always the whole file.")
	}

	return Analysis{Warnings: warnings, MaxMatchBytes: maxBytes}, nil
}

// CompileBounded compiles only patterns whose full maximum byte width is
// provably contained by maxMatchWindow. Exact chunked operations must use this
// entry point; unsupported patterns fail before source reads or output creation.
func CompileBounded(pattern []byte, caseInsensitive bool, maxMatchWindow int) (*regexp.Regexp, Analysis, error) {
	if maxMatchWindow <= 0 {
		return nil, Analysis{}, errors.New("regex match window must be positive")
	}
	if maxMatchWindow > MaxExactMatchWindowBytes {
		return nil, Analysis{}, fmt.Errorf("%w: match window %d exceeds %d bytes", ErrRegexResourceLimit, maxMatchWindow, MaxExactMatchWindowBytes)
	}
	analysis, err := Analyze(pattern, caseInsensitive, maxMatchWindow)
	if err != nil {
		return nil, Analysis{}, err
	}
	re, err := Compile(pattern, caseInsensitive)
	return re, analysis, err
}

// ValidateCompiledBounded applies the same exactness proof to an already
// compiled regex.
func ValidateCompiledBounded(re *regexp.Regexp, maxMatchWindow int) error {
	if re == nil {
		return errors.New("nil regexp")
	}
	expression := re.String()
	if len(expression) > MaxPatternBytes {
		return fmt.Errorf("%w: pattern has %d bytes, maximum is %d", ErrRegexResourceLimit, len(expression), MaxPatternBytes)
	}
	_, _, err := CompileBounded([]byte(expression), false, maxMatchWindow)
	return err
}

// Compile validates and compiles a regex pattern under Novera's bounded-regex
// policy. Patterns that can match empty text are refused because they do not
// consume input and can make search/replace counts ambiguous across chunks.
func Compile(pattern []byte, caseInsensitive bool) (*regexp.Regexp, error) {
	if err := validatePatternSize(pattern); err != nil {
		return nil, err
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

func validatePatternSize(pattern []byte) error {
	if len(pattern) == 0 {
		return errors.New("empty pattern")
	}
	if len(pattern) > MaxPatternBytes {
		return fmt.Errorf("%w: pattern has %d bytes, maximum is %d", ErrRegexResourceLimit, len(pattern), MaxPatternBytes)
	}
	return nil
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

func maximumMatchBytes(expr string, caseInsensitive bool) (maxBytes int64, unbounded bool, boundary bool, err error) {
	if caseInsensitive {
		expr = "(?i)" + expr
	}
	parsed, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return 0, false, false, err
	}
	maxBytes, unbounded, boundary = regexpWidth(parsed)
	return maxBytes, unbounded, boundary, nil
}

func regexpWidth(re *syntax.Regexp) (maxBytes int64, unbounded bool, boundary bool) {
	if re == nil {
		return 0, false, false
	}
	switch re.Op {
	case syntax.OpNoMatch, syntax.OpEmptyMatch:
		return 0, false, false
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0, false, true
	case syntax.OpLiteral:
		var total int64
		for _, r := range re.Rune {
			width := maxFoldedRuneBytes(r, re.Flags&syntax.FoldCase != 0)
			total = saturatingAdd(total, int64(width))
		}
		return total, false, false
	case syntax.OpCharClass:
		width := 1
		for i := 0; i+1 < len(re.Rune); i += 2 {
			candidate := utf8.RuneLen(re.Rune[i+1])
			if candidate < 0 {
				candidate = utf8.RuneLen(utf8.RuneError)
			}
			if candidate > width {
				width = candidate
			}
		}
		return int64(width), false, false
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return utf8.UTFMax, false, false
	case syntax.OpCapture:
		if len(re.Sub) == 0 {
			return 0, false, false
		}
		return regexpWidth(re.Sub[0])
	case syntax.OpConcat:
		var total int64
		for _, sub := range re.Sub {
			width, childUnbounded, childBoundary := regexpWidth(sub)
			boundary = boundary || childBoundary
			unbounded = unbounded || childUnbounded
			total = saturatingAdd(total, width)
		}
		return total, unbounded, boundary
	case syntax.OpAlternate:
		var widest int64
		for _, sub := range re.Sub {
			width, childUnbounded, childBoundary := regexpWidth(sub)
			boundary = boundary || childBoundary
			unbounded = unbounded || childUnbounded
			if width > widest {
				widest = width
			}
		}
		return widest, unbounded, boundary
	case syntax.OpQuest:
		width, childUnbounded, childBoundary := regexpWidth(re.Sub[0])
		return width, childUnbounded, childBoundary
	case syntax.OpStar, syntax.OpPlus:
		width, childUnbounded, childBoundary := regexpWidth(re.Sub[0])
		if width == 0 && !childUnbounded {
			return 0, false, childBoundary
		}
		return width, true, childBoundary
	case syntax.OpRepeat:
		width, childUnbounded, childBoundary := regexpWidth(re.Sub[0])
		if childUnbounded {
			return width, true, childBoundary
		}
		if re.Max < 0 {
			if width == 0 {
				return 0, false, childBoundary
			}
			return width, true, childBoundary
		}
		return saturatingMultiply(width, int64(re.Max)), false, childBoundary
	default:
		return 0, true, false
	}
}

func maxFoldedRuneBytes(r rune, folded bool) int {
	maxWidth := utf8.RuneLen(r)
	if maxWidth < 0 {
		maxWidth = utf8.RuneLen(utf8.RuneError)
	}
	if !folded {
		return maxWidth
	}
	for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
		if width := utf8.RuneLen(next); width > maxWidth {
			maxWidth = width
		}
	}
	return maxWidth
}

func saturatingAdd(a int64, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func saturatingMultiply(a int64, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
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
