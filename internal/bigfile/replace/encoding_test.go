package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/encodingx"
)

func TestConvertEncodingFileWindows1251ToUTF8(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	body, err := encodingx.EncodeString("Windows-1251", "Привет\r\nмир\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-8", FileOptions{})
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "Привет\r\nмир\r\n" {
		t.Fatalf("output = %q", string(got))
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Operation != "encoding-convert" {
		t.Fatalf("manifest operation = %q", manifest.Operation)
	}
	if manifest.Status != "complete" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
}

func TestConvertEncodingFileWritesUTF16BOM(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-16LE", FileOptions{}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0] != 0xFF || got[1] != 0xFE {
		t.Fatalf("output BOM = %v, want UTF-16LE BOM", got[:min(2, len(got))])
	}
	decoded, err := encodingx.DecodeBytes("UTF-16LE", got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "alpha\nbeta\n" {
		t.Fatalf("decoded output = %q", decoded)
	}
}

func TestConvertEncodingFileUsesWholeSourceWhileEditableSliceIsDirty(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "huge-window.log")
	outPath := filepath.Join(dir, "converted.txt")
	content := "head original\nslice original\noutside original\n"
	if err := os.WriteFile(srcPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	sliceStart := int64(len("head original\n"))
	sliceEnd := sliceStart + int64(len("slice original\n"))
	buf, err := document.LoadInMemoryWindow(doc, sliceStart, sliceEnd, sliceEnd-sliceStart)
	if err != nil {
		t.Fatal(err)
	}
	editedSlice := strings.Replace(buf.Text, "original", "edited", 1)
	if _, _, encoded, err := buf.EncodedReplacement(editedSlice); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(encoded), "original") {
		t.Fatalf("edited replacement still contains original marker: %q", string(encoded))
	}

	if _, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-16LE", FileOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := encodingx.DecodeBytes("UTF-16LE", got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != content {
		t.Fatalf("converted output = %q, want original source %q", decoded, content)
	}
	if strings.Contains(decoded, "slice edited") {
		t.Fatalf("conversion leaked dirty editable-slice text: %q", decoded)
	}
	sourceAfter, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != content {
		t.Fatalf("source changed to %q, want immutable original", string(sourceAfter))
	}
}

func TestConvertEncodingFileDropsUTF8BOMWhenSourceWasNotUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	body, err := encodingx.EncodeString("Windows-1252", "café\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

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
	if string(got) != "café\r\n" {
		t.Fatalf("output = %q", string(got))
	}
}

func TestConvertEncodingFileCancelDeletesPartial(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	body, err := encodingx.EncodeString("Windows-1252", strings.Repeat("café\r\n", 4096))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ConvertEncodingFile(ctx, srcPath, outPath, "UTF-8", FileOptions{
		DeletePartialOnCancel: true,
		Progress: func(p Progress) {
			if !canceled && p.BytesProcessed > 0 {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial temp should be deleted, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
}

func TestConvertEncodingFileCancelKeepsPartial(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	body, err := encodingx.EncodeString("Windows-1252", strings.Repeat("alpha\r\n", 4096))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ConvertEncodingFile(ctx, srcPath, outPath, "UTF-8", FileOptions{
		DeletePartialOnCancel: false,
		Progress: func(p Progress) {
			if !canceled && p.BytesProcessed > 0 {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); err != nil {
		t.Fatalf("partial temp should be preserved, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if !strings.Contains(manifest.Error, context.Canceled.Error()) {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}
