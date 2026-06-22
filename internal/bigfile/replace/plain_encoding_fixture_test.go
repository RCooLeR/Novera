package replace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/encodingx"
)

func TestReplacePlainFileFixtureWindows1251(t *testing.T) {
	srcPath := copyEncodingFixtureToTemp(t, "windows1251_crlf.txt")
	outPath := filepath.Join(t.TempDir(), "output-1251.txt")

	pattern, err := encodingx.EncodeString("Windows-1251", "мир")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := encodingx.EncodeString("Windows-1251", "земля")
	if err != nil {
		t.Fatal(err)
	}

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, pattern, replacement, FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != 1 {
		t.Fatalf("matches = %d, want 1", summary.Matches)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := encodingx.DecodeBytes("Windows-1251", got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "Привет\r\nземля\r\n" {
		t.Fatalf("decoded output = %q", decoded)
	}
}

func copyEncodingFixtureToTemp(t *testing.T, name string) string {
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
