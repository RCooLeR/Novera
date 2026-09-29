package search

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

type memReaderAt struct {
	data []byte
}

func (m memReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m memReaderAt) Size() int64 {
	return int64(len(m.data))
}

func TestFindPlainBoundary(t *testing.T) {
	r := memReaderAt{data: []byte("abcxxhello")}
	var offsets []int64

	err := FindPlain(context.Background(), r, []byte("hello"), PlainOptions{ChunkSize: 7}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 1 || offsets[0] != 5 {
		t.Fatalf("unexpected offsets: %#v", offsets)
	}
}

func TestFindPlainVeryLongSingleLineAcrossChunks(t *testing.T) {
	prefix := strings.Repeat("a", 4094)
	body := prefix + "needle" + strings.Repeat("b", 128*1024)
	r := memReaderAt{data: []byte(body)}
	var offsets []int64

	err := FindPlain(context.Background(), r, []byte("needle"), PlainOptions{ChunkSize: 4096}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 1 || offsets[0] != int64(len(prefix)) {
		t.Fatalf("offsets = %#v, want [%d]", offsets, len(prefix))
	}
}

func TestCollectPlainResultsWithPreview(t *testing.T) {
	r := memReaderAt{data: []byte("alpha hello bravo\ncharlie hello delta")}

	results, err := CollectPlain(context.Background(), r, []byte("hello"), PlainOptions{ChunkSize: 10, MaxHits: 10}, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].Offset != 6 || results[0].Length != 5 {
		t.Fatalf("first match = %#v", results[0].Match)
	}
	if results[0].Preview != "alpha hello bravo" {
		t.Fatalf("first preview = %q", results[0].Preview)
	}
	if results[1].Preview != "arlie hello delta" {
		t.Fatalf("second preview = %q", results[1].Preview)
	}
}

func TestCollectPlainRespectsMaxHits(t *testing.T) {
	r := memReaderAt{data: []byte("x x x x")}

	results, err := CollectPlain(context.Background(), r, []byte("x"), PlainOptions{ChunkSize: 2, MaxHits: 2}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
}

func TestCollectPlainStartOffsetPagesForward(t *testing.T) {
	r := memReaderAt{data: []byte("x x x x")}

	results, err := CollectPlain(context.Background(), r, []byte("x"), PlainOptions{ChunkSize: 2, MaxHits: 2, StartOffset: 2}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].Offset != 2 || results[1].Offset != 4 {
		t.Fatalf("offsets = %d, %d; want 2, 4", results[0].Offset, results[1].Offset)
	}
}

func TestFindPlainBackwardOrder(t *testing.T) {
	r := memReaderAt{data: []byte("alpha hello bravo hello charlie")}
	var offsets []int64

	err := FindPlainBackward(context.Background(), r, []byte("hello"), PlainOptions{ChunkSize: 9}, func(m Match) error {
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

func TestFindPlainBackwardBoundary(t *testing.T) {
	r := memReaderAt{data: []byte("abcxxhello")}
	var offsets []int64

	err := FindPlainBackward(context.Background(), r, []byte("hello"), PlainOptions{ChunkSize: 3}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 1 || offsets[0] != 5 {
		t.Fatalf("unexpected offsets: %#v", offsets)
	}
}

func TestFindPlainBackwardRespectsMaxHitsAndStartOffset(t *testing.T) {
	r := memReaderAt{data: []byte("x x x x")}
	var offsets []int64

	err := FindPlainBackward(context.Background(), r, []byte("x"), PlainOptions{ChunkSize: 2, MaxHits: 2, StartOffset: 5}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{4, 2}
	if len(offsets) != len(want) {
		t.Fatalf("offsets = %#v, want %#v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("offsets = %#v, want %#v", offsets, want)
		}
	}
}

func TestFindPlainBackwardWholeWord(t *testing.T) {
	r := memReaderAt{data: []byte("cat catalog bobcat cat cat_ cat.")}
	var offsets []int64

	err := FindPlainBackward(context.Background(), r, []byte("cat"), PlainOptions{ChunkSize: 8, WholeWord: true}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{28, 19, 0}
	if len(offsets) != len(want) {
		t.Fatalf("offsets = %#v, want %#v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("offsets = %#v, want %#v", offsets, want)
		}
	}
}

func TestFindPlainProgress(t *testing.T) {
	r := memReaderAt{data: []byte("one two one two")}
	var progress []Progress

	err := FindPlain(context.Background(), r, []byte("one"), PlainOptions{
		ChunkSize: 5,
		Progress: func(p Progress) {
			progress = append(progress, p)
		},
	}, func(Match) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) == 0 {
		t.Fatal("expected progress callbacks")
	}
	last := progress[len(progress)-1]
	if last.BytesProcessed != r.Size() {
		t.Fatalf("bytes processed = %d, want %d", last.BytesProcessed, r.Size())
	}
	if last.BytesTotal != r.Size() {
		t.Fatalf("bytes total = %d, want %d", last.BytesTotal, r.Size())
	}
	if last.Matches != 2 {
		t.Fatalf("matches = %d, want 2", last.Matches)
	}
}

func TestFindPlainProgressBeforeMaxHitsReturn(t *testing.T) {
	r := memReaderAt{data: []byte("x x x")}
	var progress []Progress

	err := FindPlain(context.Background(), r, []byte("x"), PlainOptions{
		ChunkSize: 2,
		MaxHits:   2,
		Progress: func(p Progress) {
			progress = append(progress, p)
		},
	}, func(Match) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) == 0 {
		t.Fatal("expected progress before max-hit return")
	}
	if got := progress[len(progress)-1].Matches; got != 2 {
		t.Fatalf("matches = %d, want 2", got)
	}
}

func TestFindPlainCaseInsensitive(t *testing.T) {
	r := memReaderAt{data: []byte("Hello hELLo nope")}
	var offsets []int64

	err := FindPlain(context.Background(), r, []byte("hello"), PlainOptions{ChunkSize: 5, CaseInsensitive: true}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 6 {
		t.Fatalf("offsets = %#v, want [0 6]", offsets)
	}
}

func TestFindPlainCaseInsensitiveUsesByteStableASCIIFold(t *testing.T) {
	text := "prefix İxx hello KELVIN Kelvin straße"
	r := memReaderAt{data: []byte(text)}
	var offsets []int64

	err := FindPlain(context.Background(), r, []byte("kelvin"), PlainOptions{ChunkSize: 9, CaseInsensitive: true}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOffset := int64(strings.Index(text, "KELVIN"))
	if len(offsets) != 1 || offsets[0] != wantOffset {
		t.Fatalf("offsets = %#v, want [%d]", offsets, wantOffset)
	}
}

func TestFindPlainBackwardCaseInsensitiveUsesByteStableASCIIFold(t *testing.T) {
	text := "prefix İxx hello KELVIN Kelvin straße"
	r := memReaderAt{data: []byte(text)}
	var offsets []int64

	err := FindPlainBackward(context.Background(), r, []byte("kelvin"), PlainOptions{ChunkSize: 7, CaseInsensitive: true}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOffset := int64(strings.Index(text, "KELVIN"))
	if len(offsets) != 1 || offsets[0] != wantOffset {
		t.Fatalf("offsets = %#v, want [%d]", offsets, wantOffset)
	}
}

func TestFindPlainWholeWord(t *testing.T) {
	r := memReaderAt{data: []byte("cat catalog bobcat cat cat_ cat.")}
	var offsets []int64

	err := FindPlain(context.Background(), r, []byte("cat"), PlainOptions{ChunkSize: 8, WholeWord: true}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0, 19, 28}
	if len(offsets) != len(want) {
		t.Fatalf("offsets = %#v, want %#v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("offsets = %#v, want %#v", offsets, want)
		}
	}
}

func TestFindPlainWholeWordAcrossBoundaryNeedsNextByte(t *testing.T) {
	r := memReaderAt{data: []byte("cat catalog CAT cat_ cat.")}
	var offsets []int64

	err := FindPlain(context.Background(), r, []byte("cat"), PlainOptions{
		ChunkSize:       6,
		CaseInsensitive: true,
		WholeWord:       true,
	}, func(m Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0, 12, 21}
	if len(offsets) != len(want) {
		t.Fatalf("offsets = %#v, want %#v", offsets, want)
	}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("offsets = %#v, want %#v", offsets, want)
		}
	}
}

func TestFindPlainWholeWordUnicodeBoundariesAreChunkInvariant(t *testing.T) {
	const supplementaryLetter = "𐐀" // Deseret capital letter; four UTF-8 bytes.
	for chunkSize := len("cat") + utf8.UTFMax; chunkSize <= 13; chunkSize++ {
		for padding := 0; padding < chunkSize; padding++ {
			data := strings.Repeat(".", padding) + supplementaryLetter + "cat cat cat" + supplementaryLetter
			want := int64(padding + len(supplementaryLetter) + len("cat "))
			name := fmt.Sprintf("chunk=%d/padding=%d", chunkSize, padding)
			t.Run(name, func(t *testing.T) {
				r := memReaderAt{data: []byte(data)}
				for _, backward := range []bool{false, true} {
					var offsets []int64
					find := FindPlain
					if backward {
						find = FindPlainBackward
					}
					err := find(context.Background(), r, []byte("cat"), PlainOptions{
						ChunkSize: chunkSize,
						WholeWord: true,
					}, func(m Match) error {
						offsets = append(offsets, m.Offset)
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if len(offsets) != 1 || offsets[0] != want {
						t.Fatalf("backward=%v offsets=%v, want [%d]", backward, offsets, want)
					}
				}
			})
		}
	}
}

func TestFindPlainWholeWordTreatsMarksAndInvalidSeamsConservatively(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want int64
	}{
		{name: "combining mark", data: []byte("e\u0301 e"), want: int64(len("e\u0301 "))},
		{name: "invalid preceding byte", data: []byte{0xff, 'e', ' ', 'e'}, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var offsets []int64
			err := FindPlain(context.Background(), memReaderAt{data: tt.data}, []byte("e"), PlainOptions{
				ChunkSize: len("e") + utf8.UTFMax,
				WholeWord: true,
			}, func(m Match) error {
				offsets = append(offsets, m.Offset)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(offsets) != 1 || offsets[0] != tt.want {
				t.Fatalf("offsets=%v, want [%d]", offsets, tt.want)
			}
		})
	}
}

func TestFindPlainHonorsUTF16CodeUnitAlignmentAcrossChunks(t *testing.T) {
	// The UTF-16LE bytes for "A" occur first at unaligned byte 1 and then at
	// aligned byte 4. Exact encoded search must never report the false seam.
	r := memReaderAt{data: []byte{0x00, 0x41, 0x00, 0x42, 0x41, 0x00}}
	for chunkSize := 2; chunkSize <= 7; chunkSize++ {
		for _, backward := range []bool{false, true} {
			var offsets []int64
			find := FindPlain
			if backward {
				find = FindPlainBackward
			}
			err := find(context.Background(), r, []byte{0x41, 0x00}, PlainOptions{
				ChunkSize:     chunkSize,
				ByteAlignment: 2,
			}, func(m Match) error {
				offsets = append(offsets, m.Offset)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(offsets) != 1 || offsets[0] != 4 {
				t.Fatalf("chunk=%d backward=%v offsets=%v, want [4]", chunkSize, backward, offsets)
			}
		}
	}
}

func TestFindPlainRejectsInvalidAlignmentContracts(t *testing.T) {
	r := memReaderAt{data: []byte("abc")}
	for name, opts := range map[string]PlainOptions{
		"unsupported alignment":  {ByteAlignment: 3},
		"unaligned pattern":      {ByteAlignment: 2},
		"fixed width whole word": {ByteAlignment: 2, WholeWord: true},
	} {
		t.Run(name, func(t *testing.T) {
			pattern := []byte("a")
			if name == "fixed width whole word" {
				pattern = []byte("aa")
			}
			if err := FindPlain(context.Background(), r, pattern, opts, func(Match) error { return nil }); err == nil {
				t.Fatal("expected alignment contract rejection")
			}
		})
	}
}
