package replace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/encodingx"
)

func TestConvertEncodingFileFixtureWindows1251ToUTF8(t *testing.T) {
	srcPath := copyFixtureToTemp(t, "windows1251_crlf.txt")
	outPath := filepath.Join(t.TempDir(), "output-utf8.txt")

	if _, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-8", FileOptions{}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(got), "\uFEFF") {
		t.Fatalf("unexpected UTF-8 BOM in %q", string(got))
	}
	if string(got) != "Привет\r\nмир\r\n" {
		t.Fatalf("output = %q", string(got))
	}
}

func TestConvertEncodingFileFixturePreservesUTF8BOM(t *testing.T) {
	srcPath := copyFixtureToTemp(t, "utf8_bom_mixed.txt")
	outPath := filepath.Join(t.TempDir(), "output-utf8-bom.txt")

	if _, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-8", FileOptions{}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 3 || got[0] != 0xEF || got[1] != 0xBB || got[2] != 0xBF {
		t.Fatalf("output BOM = %v, want UTF-8 BOM", got[:min(3, len(got))])
	}
	if string(got[3:]) != "alpha\r\nbeta\rgamma\n" {
		t.Fatalf("decoded text = %q", string(got[3:]))
	}
}

func TestConvertEncodingFileFixtureWindows1252ToUTF16BE(t *testing.T) {
	srcPath := copyFixtureToTemp(t, "windows1252_mixed.txt")
	outPath := filepath.Join(t.TempDir(), "output-utf16be.txt")

	if _, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-16BE", FileOptions{}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0] != 0xFE || got[1] != 0xFF {
		t.Fatalf("output BOM = %v, want UTF-16BE BOM", got[:min(2, len(got))])
	}
	decoded, err := encodingx.DecodeBytes("UTF-16BE", got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "café\r\nnaïve\rgroß\n" {
		t.Fatalf("decoded output = %q", decoded)
	}
}

func TestConvertLineEndingsFixtureUTF16BEPreservesBOM(t *testing.T) {
	srcPath := copyFixtureToTemp(t, "utf16be_bom_mixed.txt")
	outPath := filepath.Join(t.TempDir(), "output-utf16be-lf.txt")

	summary, err := ConvertLineEndingsFile(context.Background(), srcPath, outPath, "LF", FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != 3 {
		t.Fatalf("conversions = %d, want 3", summary.Matches)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0] != 0xFE || got[1] != 0xFF {
		t.Fatalf("output BOM = %v, want UTF-16BE BOM", got[:min(2, len(got))])
	}
	decoded, err := encodingx.DecodeBytes("UTF-16BE", got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "alpha\nbeta\ngamma\n" {
		t.Fatalf("decoded output = %q", decoded)
	}
}

func TestConvertLineEndingsFixtureWindows1252ToCRLF(t *testing.T) {
	srcPath := copyFixtureToTemp(t, "windows1252_mixed.txt")
	outPath := filepath.Join(t.TempDir(), "output-1252-crlf.txt")

	summary, err := ConvertLineEndingsFile(context.Background(), srcPath, outPath, "CRLF", FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != 3 {
		t.Fatalf("conversions = %d, want 3", summary.Matches)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := encodingx.DecodeBytes("Windows-1252", got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "café\r\nnaïve\r\ngroß\r\n" {
		t.Fatalf("decoded output = %q", decoded)
	}
}

func TestConvertEncodingFileFixtureWindows1252LargeProgress(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "encodings", "windows1252_mixed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	const repeat = 70000
	srcBody := bytes.Repeat(fixture, repeat)

	srcPath := filepath.Join(t.TempDir(), "windows1252-large.txt")
	outPath := filepath.Join(t.TempDir(), "windows1252-large-utf8.txt")
	if err := os.WriteFile(srcPath, srcBody, 0o600); err != nil {
		t.Fatal(err)
	}

	var callbacks int
	var lastBytes int64
	summary, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-8", FileOptions{
		Progress: func(p Progress) {
			callbacks++
			if p.BytesProcessed < lastBytes {
				t.Fatalf("progress regressed: %d -> %d", lastBytes, p.BytesProcessed)
			}
			lastBytes = p.BytesProcessed
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if callbacks == 0 {
		t.Fatal("expected progress callbacks")
	}
	if lastBytes != int64(len(srcBody)) {
		t.Fatalf("last progress bytes = %d, want %d", lastBytes, len(srcBody))
	}
	if summary.OutputPath != outPath {
		t.Fatalf("summary output = %q, want %q", summary.OutputPath, outPath)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded := string(got)
	if strings.Count(decoded, "\r\n") != repeat {
		t.Fatalf("CRLF count = %d, want %d", strings.Count(decoded, "\r\n"), repeat)
	}
	trimmed := strings.ReplaceAll(decoded, "\r\n", "")
	if strings.Count(trimmed, "\r") != repeat {
		t.Fatalf("CR-only count = %d, want %d", strings.Count(trimmed, "\r"), repeat)
	}
	if strings.Count(trimmed, "\n") != repeat {
		t.Fatalf("LF-only count = %d, want %d", strings.Count(trimmed, "\n"), repeat)
	}
}

func TestConvertLineEndingsFixtureWindows1252LargeCounts(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "encodings", "windows1252_mixed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	const repeat = 50000
	srcBody := bytes.Repeat(fixture, repeat)

	srcPath := filepath.Join(t.TempDir(), "windows1252-large-endings.txt")
	outPath := filepath.Join(t.TempDir(), "windows1252-large-lf.txt")
	if err := os.WriteFile(srcPath, srcBody, 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ConvertLineEndingsFile(context.Background(), srcPath, outPath, "LF", FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != int64(3*repeat) {
		t.Fatalf("conversions = %d, want %d", summary.Matches, 3*repeat)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := encodingx.DecodeBytes("Windows-1252", got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(decoded, "\r") {
		t.Fatal("expected LF-only output without CR")
	}
	if strings.Count(decoded, "\n") != 3*repeat {
		t.Fatalf("newline count = %d, want %d", strings.Count(decoded, "\n"), 3*repeat)
	}
}

func copyFixtureToTemp(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "encodings", name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
