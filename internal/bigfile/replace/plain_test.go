package replace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/document"
)

func TestReplacePlainBoundary(t *testing.T) {
	src, err := os.CreateTemp("", "q-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("abcxxhello world hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	matches, err := ReplacePlain(context.Background(), src, dst, []byte("hello"), []byte("bye"), PlainOptions{ChunkSize: 7})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 2 {
		t.Fatalf("matches = %d, want 2", matches)
	}

	if _, err := dst.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	want := "abcxxbye world bye"
	if string(got) != want {
		t.Fatalf("got %q, want %q", string(got), want)
	}
}

func TestReplacePlainCaseInsensitiveWholeWord(t *testing.T) {
	src, err := os.CreateTemp("", "q-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("Cat cat scatter cAt"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	matches, err := ReplacePlain(context.Background(), src, dst, []byte("cat"), []byte("dog"), PlainOptions{
		ChunkSize:       5,
		CaseInsensitive: true,
		WholeWord:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 3 {
		t.Fatalf("matches = %d, want 3", matches)
	}

	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "dog dog scatter dog" {
		t.Fatalf("got %q", string(got))
	}
}

func TestReplacePlainCaseInsensitiveUsesByteStableASCIIFold(t *testing.T) {
	src, err := os.CreateTemp("", "q-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	source := "prefix İxx hello KELVIN Kelvin straße"
	if _, err := src.WriteString(source); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	matches, err := ReplacePlain(context.Background(), src, dst, []byte("kelvin"), []byte("FOUND"), PlainOptions{
		ChunkSize:       8,
		CaseInsensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Fatalf("matches = %d, want 1", matches)
	}

	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	want := "prefix İxx hello FOUND Kelvin straße"
	if string(got) != want {
		t.Fatalf("got %q, want %q", string(got), want)
	}
}

func TestReplacePlainBuffersDenseMatchWrites(t *testing.T) {
	src, err := os.CreateTemp("", "q-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	source := strings.Repeat("old_database\n", 4096)
	if _, err := src.WriteString(source); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	dst := &countingSyncWriter{}
	matches, err := ReplacePlain(context.Background(), src, dst, []byte("old_database"), []byte("new_database"), PlainOptions{
		ChunkSize: 64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 4096 {
		t.Fatalf("matches = %d, want 4096", matches)
	}
	want := strings.ReplaceAll(source, "old_database", "new_database")
	if got := dst.String(); got != want {
		t.Fatalf("output length/content mismatch: got len %d want len %d", len(got), len(want))
	}
	if dst.writeCalls >= 32 {
		t.Fatalf("write calls = %d, want buffered chunk-level writes", dst.writeCalls)
	}
	if !dst.synced {
		t.Fatal("destination was not synced")
	}
}

func TestReplacePlainFileUsesWholeSourceWhileEditableSliceIsDirty(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "huge-window.log")
	outPath := filepath.Join(dir, "replaced.txt")
	content := []byte("head original\nslice original\noutside original\n")
	if err := os.WriteFile(srcPath, content, 0o600); err != nil {
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

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("original"), []byte("replaced"), FileOptions{ChunkSize: 8})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != 3 {
		t.Fatalf("matches = %d, want 3", summary.Matches)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(string(content), "original", "replaced")
	if string(got) != want {
		t.Fatalf("replace output = %q, want %q", string(got), want)
	}
	if strings.Contains(string(got), "slice edited") {
		t.Fatalf("replace leaked dirty editable-slice text: %q", string(got))
	}
	sourceAfter, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != string(content) {
		t.Fatalf("source changed to %q, want immutable original", string(sourceAfter))
	}
}

type countingSyncWriter struct {
	bytes.Buffer
	writeCalls int
	synced     bool
}

func (w *countingSyncWriter) Write(p []byte) (int, error) {
	w.writeCalls++
	return w.Buffer.Write(p)
}

func (w *countingSyncWriter) Sync() error {
	w.synced = true
	return nil
}
