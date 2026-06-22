package exportx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/encodingx"
)

func TestExportVisibleTextWritesUTF8(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "selection.txt")

	summary, err := ExportVisibleTextWithOptions(context.Background(), outPath, "alpha\nbeta", "UTF-8", Options{ComputeSHA256: true, WriteManifest: true})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Mode != "visible-selection" {
		t.Fatalf("mode = %q", summary.Mode)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\nbeta" {
		t.Fatalf("output = %q", string(got))
	}
	manifest := readExportManifest(t, summary.ManifestPath)
	if manifest.Mode != "visible-selection" || manifest.OutputPath != outPath || manifest.SHA256 == "" {
		t.Fatalf("manifest = %+v, want visible-selection output evidence", manifest)
	}
}

func TestExportVisibleTextWritesWindows1252(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "selection-1252.txt")

	if _, err := ExportVisibleText(context.Background(), outPath, "café", "Windows-1252"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := encodingx.DecodeBytes("Windows-1252", got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "café" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestExportVisibleTextWritesUTF16LEWithBOM(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "selection-utf16le.txt")

	summary, err := ExportVisibleText(context.Background(), outPath, "alpha\nbeta", "UTF-16LE")
	if err != nil {
		t.Fatal(err)
	}
	if summary.BytesWritten <= int64(len("alpha\nbeta")) {
		t.Fatalf("bytes written = %d, expected UTF-16 output with BOM", summary.BytesWritten)
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
	if decoded != "alpha\nbeta" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestExportVisibleTextPreservesReplacementRuneForUTF8(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "selection-invalid-decoded.txt")

	if _, err := ExportVisibleText(context.Background(), outPath, "bad\uFFFDbyte", "UTF-8"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bad\uFFFDbyte" {
		t.Fatalf("output = %q", string(got))
	}
}

func TestExportVisibleTextLegacyEncodingRejectsReplacementRuneWithoutOutput(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "selection-invalid-1252.txt")

	if _, err := ExportVisibleText(context.Background(), outPath, "bad\uFFFDbyte", "Windows-1252"); err == nil {
		t.Fatal("expected unsupported rune error")
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("expected no output file after encode failure, stat err = %v", err)
	}
}
