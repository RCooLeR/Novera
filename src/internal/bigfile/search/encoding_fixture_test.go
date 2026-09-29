package search

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/encodingx"
)

func TestFindPlainFixtureWindows1251Forward(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "windows1251_crlf.txt")
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	pattern, err := encodingx.EncodeString("Windows-1251", "мир")
	if err != nil {
		t.Fatal(err)
	}

	var offsets []int64
	err = FindPlain(context.Background(), doc, pattern, PlainOptions{ChunkSize: 4}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 1 || offsets[0] != 8 {
		t.Fatalf("offsets = %#v, want [8]", offsets)
	}
}

func TestFindPlainBackwardFixtureWindows1252(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "windows1252_mixed.txt")
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	var offsets []int64
	err = FindPlainBackward(context.Background(), doc, []byte("a"), PlainOptions{ChunkSize: 3}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{7, 1}
	if len(offsets) != len(want) {
		t.Fatalf("offsets = %#v, want %#v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("offsets = %#v, want %#v", offsets, want)
		}
	}
}

func TestFindPlainFixtureUTF16BEBOM(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "utf16be_bom_mixed.txt")
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	pattern, err := encodingx.EncodeString("UTF-16BE", "beta")
	if err != nil {
		t.Fatal(err)
	}

	var offsets []int64
	err = FindPlain(context.Background(), doc, pattern, PlainOptions{ChunkSize: 9}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 1 || offsets[0] != 16 {
		t.Fatalf("offsets = %#v, want [16]", offsets)
	}
}

func TestCollectPlainFixtureNonUTF8PreviewPlaceholder(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "windows1251_crlf.txt")
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	pattern, err := encodingx.EncodeString("Windows-1251", "Привет")
	if err != nil {
		t.Fatal(err)
	}

	results, err := CollectPlain(context.Background(), doc, pattern, PlainOptions{ChunkSize: 4, MaxHits: 1}, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v, want 1 match", results)
	}
	if results[0].Offset != 0 {
		t.Fatalf("offset = %d, want 0", results[0].Offset)
	}
	if results[0].Preview == "" {
		t.Fatal("expected non-empty preview placeholder")
	}
}

func TestFindPlainFixtureWindows1251LargeMaxHitsProgress(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTempRepeated(t, "windows1251_crlf.txt", 40000)
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	var progress []Progress
	var matches int
	err = FindPlain(context.Background(), doc, []byte("\r\n"), PlainOptions{
		ChunkSize: 8 * 1024,
		MaxHits:   500,
		Progress: func(p Progress) {
			progress = append(progress, p)
		},
	}, func(m Match) error {
		matches++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 500 {
		t.Fatalf("matches = %d, want 500", matches)
	}
	if len(progress) == 0 {
		t.Fatal("expected progress callbacks")
	}
	last := progress[len(progress)-1]
	if last.Matches != 500 {
		t.Fatalf("last progress matches = %d, want 500", last.Matches)
	}
	if last.BytesProcessed <= 0 || last.BytesProcessed > doc.Size() {
		t.Fatalf("last progress bytes = %d, doc size = %d", last.BytesProcessed, doc.Size())
	}
	if last.BytesProcessed >= doc.Size() {
		t.Fatalf("expected max-hit early stop before full scan, got %d/%d", last.BytesProcessed, doc.Size())
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

func copyReplaceEncodingFixtureToTempRepeated(t *testing.T, name string, repeat int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "replace", "testdata", "encodings", name))
	if err != nil {
		t.Fatal(err)
	}
	if repeat < 1 {
		repeat = 1
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, bytes.Repeat(data, repeat), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
