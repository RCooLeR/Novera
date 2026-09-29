package document

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenFileDetectsUTF8WhenSampleEndsInsideRune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "split-rune.txt")
	content := bytes.Repeat([]byte{'a'}, openSampleSize-1)
	content = append(content, []byte("€ tail")...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	if got := doc.Metadata().Encoding; got != "UTF-8" {
		t.Fatalf("encoding = %q, want UTF-8", got)
	}
	if doc.Metadata().Binary {
		t.Fatal("valid UTF-8 prefix seam was classified as binary")
	}
}
