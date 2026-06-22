package exportx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/encodingx"
)

func TestExportLineRangeTextFixtureWindows1251ToUTF16LE(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "windows1251_crlf.txt")
	outPath := filepath.Join(t.TempDir(), "line2-utf16le.txt")

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	sourceEncoding := doc.Metadata().Encoding
	summary, err := ExportLineRangeText(context.Background(), doc, srcPath, outPath, 2, 2, sourceEncoding, "UTF-16LE", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.UsedLineRange || summary.Mode != "line-range-text" {
		t.Fatalf("summary = %#v", summary)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0] != 0xFF || got[1] != 0xFE {
		t.Fatalf("output BOM = %v, want UTF-16LE BOM", got[:minInt(2, len(got))])
	}
	decoded, err := encodingx.DecodeBytes("UTF-16LE", got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "мир\r\n" {
		t.Fatalf("decoded output = %q", decoded)
	}
}

func TestExportByteRangeTextFixtureUTF16BEStripsSourceBOM(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "utf16be_bom_mixed.txt")
	outPath := filepath.Join(t.TempDir(), "utf16be-to-utf8.txt")

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	sourceEncoding := doc.Metadata().Encoding
	summary, err := ExportByteRangeText(context.Background(), doc, srcPath, outPath, 0, doc.Size(), sourceEncoding, "UTF-8", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Mode != "byte-range-text" {
		t.Fatalf("summary mode = %q", summary.Mode)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(got), "\uFEFF") {
		t.Fatalf("unexpected UTF-8 BOM in %q", string(got))
	}
	if string(got) != "alpha\r\nbeta\rgamma\n" {
		t.Fatalf("output = %q", string(got))
	}
}

func TestExportVisibleRangeTextFixtureWindows1252ToUTF8(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "windows1252_mixed.txt")
	outPath := filepath.Join(t.TempDir(), "visible-utf8.txt")

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	sourceEncoding := doc.Metadata().Encoding
	if _, err := ExportVisibleRangeText(context.Background(), doc, srcPath, outPath, 0, doc.Size(), sourceEncoding, "UTF-8", Options{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "café\r\nnaïve\rgroß\n" {
		t.Fatalf("output = %q", string(got))
	}
}

func copyReplaceEncodingFixtureToTemp(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "replace", "testdata", "encodings", name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
