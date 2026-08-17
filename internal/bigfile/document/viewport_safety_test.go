package document

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestVisiblePageRejectsHostileLimitsBeforeAllocation(t *testing.T) {
	doc := openDocumentBytes(t, []byte("one\ntwo\n"))

	tests := []struct {
		name  string
		count int
		opts  VisibleLineOptions
	}{
		{name: "line count", count: maxVisiblePageLines + 1},
		{name: "page bytes", count: 1, opts: VisibleLineOptions{MaxBytes: maxVisiblePageBytes + 1}},
		{name: "line bytes", count: 1, opts: VisibleLineOptions{MaxLineBytes: maxVisibleLineBytes + 1}},
		{name: "long-line probe", count: 1, opts: VisibleLineOptions{LongLineLimitBytes: maxLongLineProbeBytes + 1}},
		{name: "negative horizontal", count: 1, opts: VisibleLineOptions{HorizontalByteOffset: -1}},
		{name: "line-number overflow", count: 2, opts: VisibleLineOptions{FirstLineNumber: math.MaxInt64}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := doc.VisiblePageFromOffset(0, tt.count, tt.opts)
			if !errors.Is(err, ErrVisiblePageLimit) {
				t.Fatalf("error = %v, want ErrVisiblePageLimit", err)
			}
			if len(page.Lines) != 0 {
				t.Fatalf("hostile request materialized lines: %#v", page)
			}
		})
	}

	page, err := doc.VisiblePageFromOffset(0, 1, VisibleLineOptions{
		MaxBytes:             16,
		MaxLineBytes:         8,
		HorizontalByteOffset: math.MaxInt,
	})
	if err != nil {
		t.Fatalf("large non-allocating horizontal offset: %v", err)
	}
	if len(page.Lines) != 1 || page.Lines[0].Text != "" || page.Lines[0].DisplayOffset != int64(len("one")) {
		t.Fatalf("large horizontal offset page = %#v", page)
	}
}

func TestVisiblePageDoesNotSplitEncodedCharacter(t *testing.T) {
	t.Run("UTF-8", func(t *testing.T) {
		doc := openDocumentBytes(t, []byte("éx\n"))
		page, err := doc.VisiblePageFromOffset(0, 2, VisibleLineOptions{MaxBytes: 8, MaxLineBytes: 1})
		if !errors.Is(err, ErrVisiblePageLimit) {
			t.Fatalf("error = %v, want character-budget error", err)
		}
		if len(page.Lines) != 0 {
			t.Fatalf("partial UTF-8 rune was rendered: %#v", page.Lines)
		}
	})

	t.Run("UTF-16 surrogate", func(t *testing.T) {
		data := []byte{
			0xff, 0xfe,
			0x3d, 0xd8, 0x00, 0xde, // U+1F600
			'x', 0,
			'\n', 0,
		}
		doc := openDocumentBytes(t, data)
		page, err := doc.VisiblePageFromOffset(0, 2, VisibleLineOptions{MaxBytes: 16, MaxLineBytes: 4})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Lines) != 2 || page.Lines[0].Text != "" || page.Lines[1].Text != "😀" {
			t.Fatalf("UTF-16 page split a surrogate: %#v", page.Lines)
		}
	})
}

func TestVisiblePageAlignsHorizontalStartForward(t *testing.T) {
	t.Run("UTF-8 continuation", func(t *testing.T) {
		doc := openDocumentBytes(t, []byte("éx\n"))
		page, err := doc.VisiblePageFromOffset(0, 1, VisibleLineOptions{
			MaxBytes:             8,
			MaxLineBytes:         8,
			HorizontalByteOffset: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Lines) != 1 || page.Lines[0].Text != "x" || page.Lines[0].DisplayOffset != 2 {
			t.Fatalf("UTF-8 horizontal page = %#v", page)
		}
	})

	t.Run("UTF-16 low surrogate", func(t *testing.T) {
		data := []byte{
			0xff, 0xfe,
			0x3d, 0xd8, 0x00, 0xde,
			'x', 0,
			'\n', 0,
		}
		doc := openDocumentBytes(t, data)
		page, err := doc.VisiblePageFromOffset(0, 1, VisibleLineOptions{
			MaxBytes:             16,
			MaxLineBytes:         16,
			HorizontalByteOffset: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Lines) != 1 || page.Lines[0].Text != "x" || page.Lines[0].DisplayOffset != 6 {
			t.Fatalf("UTF-16 horizontal page = %#v", page)
		}
	})
}

func TestTextOffsetAlignmentAndStrictDecode(t *testing.T) {
	doc := openDocumentBytes(t, []byte("aéb"))
	back, err := doc.AlignTextOffsetBackward(2)
	if err != nil || back != 1 {
		t.Fatalf("backward alignment = %d, %v; want 1", back, err)
	}
	forward, err := doc.AlignTextOffsetForward(2)
	if err != nil || forward != 3 {
		t.Fatalf("forward alignment = %d, %v; want 3", forward, err)
	}
	if _, err := doc.DecodeAlignedRange(2, 3, 4); !errors.Is(err, ErrTextBoundary) {
		t.Fatalf("unaligned decode error = %v, want ErrTextBoundary", err)
	}
	if got, err := doc.DecodeAlignedRange(1, 3, 4); err != nil || got != "é" {
		t.Fatalf("aligned decode = %q, %v", got, err)
	}
}

func TestPreviousWindowStartRejectsUnboundedRequests(t *testing.T) {
	doc := openDocumentBytes(t, []byte("one\ntwo\n"))
	if _, err := doc.PreviousWindowStart(doc.Size(), maxVisiblePageBytes+1, 1); !errors.Is(err, ErrVisiblePageLimit) {
		t.Fatalf("byte limit error = %v, want ErrVisiblePageLimit", err)
	}
	if _, err := doc.PreviousWindowStart(doc.Size(), 16, maxVisiblePageLines+1); !errors.Is(err, ErrVisiblePageLimit) {
		t.Fatalf("line limit error = %v, want ErrVisiblePageLimit", err)
	}
}

func TestBoundedWindowNavigationUsesLogicalLineStarts(t *testing.T) {
	doc := openDocumentBytes(t, []byte("one\ntwo\nthree"))

	start, continues, err := doc.AlignedWindowStart(6, 16)
	if err != nil || continues || start != 4 {
		t.Fatalf("aligned line start = %d, continues=%v, err=%v; want 4, false", start, continues, err)
	}
	start, continues, err = doc.AlignedWindowStart(6, 1)
	if err != nil || !continues || start != 6 {
		t.Fatalf("bounded continuation = %d, continues=%v, err=%v; want 6, true", start, continues, err)
	}
	previous, err := doc.PreviousWindowStart(doc.Size(), 32, 2)
	if err != nil || previous != 4 {
		t.Fatalf("previous two-line window = %d, %v; want 4", previous, err)
	}
}

func TestReadRangeRejectsShortReadAfterSourceTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	if err := os.Truncate(path, 2); err != nil {
		t.Fatal(err)
	}
	if data, err := doc.ReadRangeWithLimit(0, 6, 6); !errors.Is(err, ErrSourceChanged) || data != nil {
		t.Fatalf("short read = %q, %v; want nil, ErrSourceChanged", data, err)
	}
}

func openDocumentBytes(t *testing.T, data []byte) *FileDocument {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = doc.Close() })
	return doc
}
