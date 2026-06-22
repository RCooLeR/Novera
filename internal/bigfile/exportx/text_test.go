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

func TestExportLineRangeTextTranscodes(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "out-1251.txt")
	if err := os.WriteFile(srcPath, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	summary, err := ExportLineRangeText(context.Background(), doc, srcPath, outPath, 2, 3, "UTF-8", "Windows-1251", Options{ComputeSHA256: true, WriteManifest: true})
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
	decoded, err := encodingx.DecodeBytes("Windows-1251", got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "two\nthree\n" {
		t.Fatalf("decoded = %q", decoded)
	}
	manifest := readExportManifest(t, summary.ManifestPath)
	if manifest.Mode != "line-range-text" || manifest.SourceEncoding != "UTF-8" || manifest.TargetEncoding != "Windows-1251" {
		t.Fatalf("manifest = %+v, want line-range text encodings", manifest)
	}
	if manifest.StartLine != 2 || manifest.EndLine != 3 || manifest.SHA256 == "" {
		t.Fatalf("manifest line/checksum evidence = %+v", manifest)
	}
}

func TestExportByteRangeTextStripsLeadingSourceBOM(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source-bom.txt")
	outPath := filepath.Join(dir, "out.txt")
	content := append([]byte{0xEF, 0xBB, 0xBF}, []byte("alpha\nbeta\n")...)
	if err := os.WriteFile(srcPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	if _, err := ExportByteRangeText(context.Background(), doc, srcPath, outPath, 0, doc.Size(), "UTF-8", "UTF-8", Options{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\nbeta\n" {
		t.Fatalf("output = %q", string(got))
	}
}

func TestExportVisibleRangeTextCancelDeletesOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("abcdef", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	_, err = ExportVisibleRangeText(ctx, doc, srcPath, outPath, 0, doc.Size(), "UTF-8", "UTF-16LE", Options{
		Progress: func(done int64, total int64) {
			if done > 0 {
				cancel()
			}
		},
	})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected output cleanup on cancel, stat err = %v", statErr)
	}
}

func TestExportByteRangeTextInvalidTargetEncodingDeletesOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(srcPath, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	if _, err := ExportByteRangeText(context.Background(), doc, srcPath, outPath, 0, doc.Size(), "UTF-8", "not-an-encoding", Options{}); err == nil {
		t.Fatal("expected unsupported target encoding error")
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected output cleanup after encoding setup failure, stat err = %v", statErr)
	}
}
