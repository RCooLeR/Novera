package regexutil

import (
	"errors"
	"regexp/syntax"
	"strings"
	"testing"
)

func TestAnalyzeRejectsLookbehind(t *testing.T) {
	_, err := Analyze([]byte(`(?<=foo)bar`), false, 1024)
	if err == nil {
		t.Fatal("expected lookbehind rejection")
	}
}

func TestAnalyzeRejectsBackreference(t *testing.T) {
	_, err := Analyze([]byte(`(foo)\1`), false, 1024)
	if err == nil {
		t.Fatal("expected backreference rejection")
	}
}

func TestAnalyzeRejectsUnboundedPatternsInExactMode(t *testing.T) {
	for _, pattern := range []string{`foo.*bar`, `[^\n]+`, `(?:.|\n)+`, `a{1,}`} {
		t.Run(pattern, func(t *testing.T) {
			if _, err := Analyze([]byte(pattern), false, 1024*1024); !errors.Is(err, ErrUnboundedRegex) {
				t.Fatalf("error = %v, want ErrUnboundedRegex", err)
			}
		})
	}
}

func TestAnalyzeRejectsChunkBoundaryAssertions(t *testing.T) {
	for _, pattern := range []string{`^CREATE`, `END$`, `\Aheader`, `footer\z`, `\bword\b`, `\Bword`} {
		t.Run(pattern, func(t *testing.T) {
			if _, err := Analyze([]byte(pattern), false, 1024*1024); !errors.Is(err, ErrRegexBoundaryContext) {
				t.Fatalf("error = %v, want ErrRegexBoundaryContext", err)
			}
		})
	}
}

func TestAnalyzeDoesNotWarnForLiteralAnchorCharacters(t *testing.T) {
	analysis, err := Analyze([]byte(`[\^$] value \\A`), false, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range analysis.Warnings {
		if strings.Contains(warning, "anchors") {
			t.Fatalf("unexpected anchor warning: %#v", analysis.Warnings)
		}
	}
}

func TestAnalyzeRejectsEmptyMatch(t *testing.T) {
	_, err := Analyze([]byte(`a*`), false, 1024)
	if err == nil {
		t.Fatal("expected empty-match rejection")
	}
}

func TestAnalyzeComputesConservativeMaximumUTF8Width(t *testing.T) {
	tests := []struct {
		pattern         string
		caseInsensitive bool
		want            int64
	}{
		{pattern: `abc`, want: 3},
		{pattern: `[0-9]{5}`, want: 5},
		{pattern: `(?:ab|cdef)`, want: 4},
		{pattern: `.{2}`, want: 8},
		{pattern: `K`, caseInsensitive: true, want: 3},
		{pattern: `[K]`, caseInsensitive: true, want: 3},
		{pattern: `[A-Z]`, caseInsensitive: true, want: 3},
		{pattern: `\p{Han}`, want: 4},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			analysis, err := Analyze([]byte(tt.pattern), tt.caseInsensitive, 64)
			if err != nil {
				t.Fatal(err)
			}
			if analysis.MaxMatchBytes != tt.want {
				t.Fatalf("max bytes = %d, want %d", analysis.MaxMatchBytes, tt.want)
			}
		})
	}
}

func TestCompileBoundedUsesUTF8BytesForCaseFoldedMatches(t *testing.T) {
	pattern := []byte(`K[0-9]{2}`)
	if _, _, err := CompileBounded(pattern, true, 4); !errors.Is(err, ErrRegexExceedsWindow) {
		t.Fatalf("window-1 error = %v, want ErrRegexExceedsWindow", err)
	}
	re, analysis, err := CompileBounded(pattern, true, 5)
	if err != nil {
		t.Fatal(err)
	}
	match := []byte("K42")
	loc := re.FindIndex(match)
	if loc == nil || loc[0] != 0 || loc[1] != len(match) {
		t.Fatalf("case-folded match = %#v", loc)
	}
	if analysis.MaxMatchBytes != int64(len(match)) {
		t.Fatalf("maximum = %d, want %d UTF-8 bytes", analysis.MaxMatchBytes, len(match))
	}
}

func TestRegexpWidthSaturatesNestedFiniteRepetitions(t *testing.T) {
	re := &syntax.Regexp{
		Op:  syntax.OpRepeat,
		Max: 1_000_000_000,
		Sub: []*syntax.Regexp{{
			Op:  syntax.OpRepeat,
			Max: 1_000_000_000,
			Sub: []*syntax.Regexp{{
				Op:   syntax.OpLiteral,
				Rune: []rune{'x'},
			}},
		}},
	}
	width, unbounded, boundary := regexpWidth(re)
	if width <= 0 || unbounded || boundary {
		t.Fatalf("width=%d unbounded=%v boundary=%v", width, unbounded, boundary)
	}
	if got := saturatingMultiply(width, 1_000_000_000); got <= 0 {
		t.Fatalf("saturated multiplication wrapped: %d", got)
	}
}

func TestCompileBoundedRejectsBoundaryAssertionsUnderNesting(t *testing.T) {
	for _, pattern := range []string{`(?:ab|^cd){2}`, `x(?m:$)`, `(?:\Bfoo){1,2}`} {
		if _, _, err := CompileBounded([]byte(pattern), false, 64); !errors.Is(err, ErrRegexBoundaryContext) {
			t.Fatalf("pattern %q error = %v, want ErrRegexBoundaryContext", pattern, err)
		}
	}
}

func TestCompileBoundedRejectsMaximumAboveWindow(t *testing.T) {
	if _, _, err := CompileBounded([]byte(`[0-9]{5}`), false, 4); !errors.Is(err, ErrRegexExceedsWindow) {
		t.Fatalf("error = %v, want ErrRegexExceedsWindow", err)
	}
	re, analysis, err := CompileBounded([]byte(`[0-9]{5}`), false, 5)
	if err != nil || re == nil || analysis.MaxMatchBytes != 5 {
		t.Fatalf("compile = %v, %#v, %v", re, analysis, err)
	}
}

func TestRegexPatternByteLimitBoundaries(t *testing.T) {
	if err := validatePatternSize(make([]byte, MaxPatternBytes)); err != nil {
		t.Fatalf("maximum pattern size rejected: %v", err)
	}
	oversized := make([]byte, MaxPatternBytes+1)
	if err := validatePatternSize(oversized); !errors.Is(err, ErrRegexResourceLimit) {
		t.Fatalf("size validation error = %v, want ErrRegexResourceLimit", err)
	}
	if _, err := Compile(oversized, false); !errors.Is(err, ErrRegexResourceLimit) {
		t.Fatalf("compile error = %v, want ErrRegexResourceLimit", err)
	}
}

func TestCompileBoundedRejectsOversizedOperationalWindow(t *testing.T) {
	if _, _, err := CompileBounded([]byte(`x`), false, MaxExactMatchWindowBytes+1); !errors.Is(err, ErrRegexResourceLimit) {
		t.Fatalf("error = %v, want ErrRegexResourceLimit", err)
	}
}
