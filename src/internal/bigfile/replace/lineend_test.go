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

func TestConvertLineEndingsFileUTF8(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("alpha\r\nbeta\rgamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}

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
	if string(got) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("output = %q", string(got))
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Operation != "line-ending-convert" {
		t.Fatalf("manifest operation = %q", manifest.Operation)
	}
	if manifest.Status != "complete" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
}

func TestConvertLineEndingsFileUsesWholeSourceWhileEditableSliceIsDirty(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "huge-window.log")
	outPath := filepath.Join(dir, "lineend.txt")
	content := "head original\r\nslice original\r\noutside original\r\n"
	if err := os.WriteFile(srcPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	sliceStart := int64(len("head original\r\n"))
	sliceEnd := sliceStart + int64(len("slice original\r\n"))
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

	if _, err := ConvertLineEndingsFile(context.Background(), srcPath, outPath, "LF", FileOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(content, "\r\n", "\n")
	if string(got) != want {
		t.Fatalf("line-ending output = %q, want %q", string(got), want)
	}
	if strings.Contains(string(got), "slice edited") {
		t.Fatalf("line-ending conversion leaked dirty editable-slice text: %q", string(got))
	}
	sourceAfter, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != content {
		t.Fatalf("source changed to %q, want immutable original", string(sourceAfter))
	}
}

func TestConvertLineEndingsFilePreservesUTF16LEBOM(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "utf16.txt")
	outPath := filepath.Join(dir, "output.txt")
	body, err := encodingx.EncodeString("UTF-16LE", "alpha\r\nbeta\n")
	if err != nil {
		t.Fatal(err)
	}
	body = append([]byte{0xFF, 0xFE}, body...)
	if err := os.WriteFile(srcPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ConvertLineEndingsFile(context.Background(), srcPath, outPath, "CRLF", FileOptions{}); err != nil {
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
	if decoded != "alpha\r\nbeta\r\n" {
		t.Fatalf("decoded output = %q", decoded)
	}
}

func TestConvertLineEndingsFileCancelDeletesPartial(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("a\r\nb\r\nc\r\nd\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ConvertLineEndingsFile(ctx, srcPath, outPath, "LF", FileOptions{
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

func TestConvertLineEndingsFileCancelKeepsPartial(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("a\r\nb\r\nc\r\nd\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ConvertLineEndingsFile(ctx, srcPath, outPath, "LF", FileOptions{
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

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
