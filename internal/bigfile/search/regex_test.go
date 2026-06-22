package search

import (
	"context"
	"io"
	"testing"
)

func TestCollectRegexpResultsWithPreview(t *testing.T) {
	r := memReaderAt{data: []byte("alpha h.llo bravo\ncharlie heLLo delta")}

	results, err := CollectRegexp(context.Background(), r, []byte("h.llo"), RegexOptions{
		ChunkSize:       8,
		MaxHits:         10,
		CaseInsensitive: true,
		MaxMatchWindow:  8,
	}, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].Offset != 6 || results[0].Length != 5 {
		t.Fatalf("first match = %#v", results[0].Match)
	}
	if results[0].Preview != "alpha h.llo bravo" {
		t.Fatalf("first preview = %q", results[0].Preview)
	}
	if results[1].Offset != 26 {
		t.Fatalf("second offset = %d, want 26", results[1].Offset)
	}
}

func TestFindRegexpBoundaryAcrossChunks(t *testing.T) {
	r := memReaderAt{data: []byte("abcxxheLLo")}
	re, err := CompileRegexpForTesting([]byte("h.llo"), true)
	if err != nil {
		t.Fatal(err)
	}

	var offsets []int64
	err = FindRegexp(context.Background(), r, re, RegexOptions{
		ChunkSize:      6,
		MaxMatchWindow: 6,
	}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 1 || offsets[0] != 5 {
		t.Fatalf("offsets = %#v, want [5]", offsets)
	}
}

func TestFindRegexpBackwardOrder(t *testing.T) {
	r := memReaderAt{data: []byte("alpha hello bravo hxllo charlie")}
	re, err := CompileRegexpForTesting([]byte("h.llo"), false)
	if err != nil {
		t.Fatal(err)
	}

	var offsets []int64
	err = FindRegexpBackward(context.Background(), r, re, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
	}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{18, 6}
	if len(offsets) != len(want) {
		t.Fatalf("offsets = %#v, want %#v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("offsets = %#v, want %#v", offsets, want)
		}
	}
}

func TestFindRegexpRejectsEmptyMatchPatterns(t *testing.T) {
	if _, err := CompileRegexpForTesting([]byte("a*"), false); err == nil {
		t.Fatal("expected empty-match regex rejection")
	}
}

func TestCollectRegexpRejectsEmptyMatchPatterns(t *testing.T) {
	r := memReaderAt{data: []byte("hello")}
	if _, err := CollectRegexp(context.Background(), r, []byte(`\b`), RegexOptions{}, 8); err == nil {
		t.Fatal("expected empty-match regex rejection")
	}
}

func TestFindRegexpProgressAndMaxHits(t *testing.T) {
	r := memReaderAt{data: []byte("ab1 ab2 ab3")}
	re, err := CompileRegexpForTesting([]byte("ab[0-9]"), false)
	if err != nil {
		t.Fatal(err)
	}

	var progress []Progress
	var offsets []int64
	err = FindRegexp(context.Background(), r, re, RegexOptions{
		ChunkSize:      4,
		MaxHits:        2,
		MaxMatchWindow: 4,
		Progress: func(p Progress) {
			progress = append(progress, p)
		},
	}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 2 {
		t.Fatalf("offsets = %#v, want 2 matches", offsets)
	}
	if len(progress) == 0 {
		t.Fatal("expected progress callbacks")
	}
	if got := progress[len(progress)-1].Matches; got != 2 {
		t.Fatalf("progress matches = %d, want 2", got)
	}
}

func TestFindRegexpReadsOneWindowPerChunk(t *testing.T) {
	r := &countingReaderAt{data: []byte("ab1 ab2 ab3")}
	re, err := CompileRegexpForTesting([]byte("ab[0-9]"), false)
	if err != nil {
		t.Fatal(err)
	}

	err = FindRegexp(context.Background(), r, re, RegexOptions{
		ChunkSize:      4,
		MaxMatchWindow: 4,
	}, func(Match) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.reads != 3 {
		t.Fatalf("ReadAt calls = %d, want one window read per chunk (3)", r.reads)
	}
}

func TestFindRegexpBackwardReadsOneWindowPerChunk(t *testing.T) {
	r := &countingReaderAt{data: []byte("ab1 ab2 ab3")}
	re, err := CompileRegexpForTesting([]byte("ab[0-9]"), false)
	if err != nil {
		t.Fatal(err)
	}

	err = FindRegexpBackward(context.Background(), r, re, RegexOptions{
		ChunkSize:      4,
		MaxMatchWindow: 4,
	}, func(Match) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.reads != 3 {
		t.Fatalf("ReadAt calls = %d, want one window read per chunk (3)", r.reads)
	}
}

type countingReaderAt struct {
	data  []byte
	reads int
}

func (r *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r *countingReaderAt) Size() int64 {
	return int64(len(r.data))
}
