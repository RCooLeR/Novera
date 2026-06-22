package exportx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/document"
)

func TestCopyByteRangeReadsSelectedText(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(srcPath, []byte("alpha\nbravo\ncharlie\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	summary, text, err := CopyByteRange(context.Background(), doc, 6, 12, DefaultClipboardMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Mode != "byte-range" {
		t.Fatalf("mode = %q, want byte-range", summary.Mode)
	}
	if summary.BytesRead != 6 {
		t.Fatalf("bytes read = %d, want 6", summary.BytesRead)
	}
	if text != "bravo\n" {
		t.Fatalf("text = %q, want bravo\\n", text)
	}
}

func TestCopyLineRangeDecodesWindows1252(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	encoded := []byte("alpha\r\ncaf\xe9\r\nna\xefve\r\n")
	if err := os.WriteFile(srcPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	summary, text, err := CopyLineRange(context.Background(), doc, 2, 3, DefaultClipboardMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.UsedLineRange {
		t.Fatal("expected line-range metadata")
	}
	if text != "caf\u00e9\r\nna\u00efve\r\n" {
		t.Fatalf("text = %q, want decoded line range", text)
	}
}

func TestCopyByteRangeRejectsOversizedClipboardReads(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(srcPath, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	_, _, err = CopyByteRange(context.Background(), doc, 0, doc.Size(), 4)
	var tooLarge *RangeTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("err = %v, want RangeTooLargeError", err)
	}
	if tooLarge.RequestedBytes != 10 || tooLarge.MaxBytes != 4 {
		t.Fatalf("tooLarge = %+v, want requested=10 max=4", tooLarge)
	}
}
