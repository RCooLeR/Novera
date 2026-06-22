package search

import (
	"context"
	"testing"

	"novera/internal/bigfile/document"
)

func TestFindRegexpFixtureWindows1251LineEndings(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "windows1251_crlf.txt")
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	re, err := CompileRegexpForTesting([]byte("\r\n"), false)
	if err != nil {
		t.Fatal(err)
	}

	var offsets []int64
	err = FindRegexp(context.Background(), doc, re, RegexOptions{ChunkSize: 4, MaxMatchWindow: 4}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{6, 11}
	assertOffsets(t, offsets, want)
}

func TestFindRegexpBackwardFixtureWindows1252LineEndings(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "windows1252_mixed.txt")
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	re, err := CompileRegexpForTesting([]byte("\r\n|\r|\n"), false)
	if err != nil {
		t.Fatal(err)
	}

	var offsets []int64
	err = FindRegexpBackward(context.Background(), doc, re, RegexOptions{ChunkSize: 4, MaxMatchWindow: 4}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{16, 11, 4}
	assertOffsets(t, offsets, want)
}

func TestFindRegexpFixtureUTF16BELineEndings(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "utf16be_bom_mixed.txt")
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	re, err := CompileRegexpForTesting([]byte("\x00\r\x00\n|\x00\r|\x00\n"), false)
	if err != nil {
		t.Fatal(err)
	}

	var offsets []int64
	err = FindRegexp(context.Background(), doc, re, RegexOptions{ChunkSize: 8, MaxMatchWindow: 8}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{12, 24, 36}
	assertOffsets(t, offsets, want)
}

func TestCollectRegexpFixtureWindows1251PreviewPlaceholder(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTemp(t, "windows1251_crlf.txt")
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	results, err := CollectRegexp(context.Background(), doc, []byte("\r\n"), RegexOptions{
		ChunkSize:      4,
		MaxHits:        1,
		MaxMatchWindow: 4,
	}, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v, want 1", results)
	}
	if results[0].Offset != 6 {
		t.Fatalf("offset = %d, want 6", results[0].Offset)
	}
	if results[0].Preview == "" {
		t.Fatal("expected non-empty preview")
	}
}

func TestFindRegexpFixtureWindows1252LargeMaxHitsProgress(t *testing.T) {
	srcPath := copyReplaceEncodingFixtureToTempRepeated(t, "windows1252_mixed.txt", 30000)
	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	re, err := CompileRegexpForTesting([]byte("\r\n|\r|\n"), false)
	if err != nil {
		t.Fatal(err)
	}

	var progress []Progress
	var matches int
	err = FindRegexp(context.Background(), doc, re, RegexOptions{
		ChunkSize:      8 * 1024,
		MaxMatchWindow: 8 * 1024,
		MaxHits:        1200,
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
	if matches != 1200 {
		t.Fatalf("matches = %d, want 1200", matches)
	}
	if len(progress) == 0 {
		t.Fatal("expected progress callbacks")
	}
	last := progress[len(progress)-1]
	if last.Matches != 1200 {
		t.Fatalf("last progress matches = %d, want 1200", last.Matches)
	}
	if last.BytesProcessed <= 0 || last.BytesProcessed > doc.Size() {
		t.Fatalf("last progress bytes = %d, doc size = %d", last.BytesProcessed, doc.Size())
	}
	if last.BytesProcessed >= doc.Size() {
		t.Fatalf("expected max-hit early stop before full scan, got %d/%d", last.BytesProcessed, doc.Size())
	}
}

func assertOffsets(t *testing.T, got []int64, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("offsets = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("offsets = %#v, want %#v", got, want)
		}
	}
}
