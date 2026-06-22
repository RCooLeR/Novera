package regexutil

import (
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

func TestAnalyzeWarnsAboutBroadWildcardsAndWordBoundaries(t *testing.T) {
	analysis, err := Analyze([]byte(`\bfoo.*bar\b`), false, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Warnings) < 2 {
		t.Fatalf("warnings = %#v, want at least 2 warnings", analysis.Warnings)
	}
	if got, want := analysis.Warnings[0], "Pattern contains broad wildcard quantifiers and may exceed the current 1.0 MiB regex window."; got != want {
		t.Fatalf("wildcard warning = %q, want %q", got, want)
	}
}

func TestAnalyzeWarnsAboutChunkBoundaryAnchors(t *testing.T) {
	for _, pattern := range []string{`^CREATE`, `END$`, `\Aheader`, `footer\z`} {
		t.Run(pattern, func(t *testing.T) {
			analysis, err := Analyze([]byte(pattern), false, 1024*1024)
			if err != nil {
				t.Fatal(err)
			}
			if len(analysis.Warnings) == 0 || analysis.Warnings[len(analysis.Warnings)-1] != "Pattern uses anchors (^, $, \\A, or \\z); bounded chunked scans evaluate them against the loaded regex window, not always the whole file." {
				t.Fatalf("warnings = %#v", analysis.Warnings)
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
