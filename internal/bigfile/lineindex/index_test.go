package lineindex

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestBuildRecordsSparseEntries(t *testing.T) {
	idx := New(2)
	if err := idx.Build(context.Background(), strings.NewReader("a\nb\nc\nd\n")); err != nil {
		t.Fatal(err)
	}

	entries := idx.Entries()
	if len(entries) != 3 {
		t.Fatalf("entries = %#v, want 3 entries", entries)
	}
	if entries[0] != (Entry{Line: 1, Offset: 0}) {
		t.Fatalf("first entry = %#v", entries[0])
	}
	if entries[1] != (Entry{Line: 3, Offset: 4}) {
		t.Fatalf("second entry = %#v", entries[1])
	}
	if entries[2] != (Entry{Line: 5, Offset: 8}) {
		t.Fatalf("third entry = %#v", entries[2])
	}
	if !idx.Done() {
		t.Fatal("index should be done")
	}
}

func TestEveryLinesForSizeKeepsSmallFilesAtDefault(t *testing.T) {
	if got := EveryLinesForSize(1 << 30); got != defaultEveryLines {
		t.Fatalf("EveryLinesForSize(1GiB) = %d, want default %d", got, defaultEveryLines)
	}
}

func TestEveryLinesForSizeScalesHugeShortLineFiles(t *testing.T) {
	got := EveryLinesForSize(500 << 30)
	if got < 65536 {
		t.Fatalf("EveryLinesForSize(500GiB) = %d, want at least 65536", got)
	}
	if got&(got-1) != 0 {
		t.Fatalf("EveryLinesForSize(500GiB) = %d, want power-of-two stride", got)
	}
}

func TestApproxLineToOffset(t *testing.T) {
	idx := New(2)
	if err := idx.Build(context.Background(), strings.NewReader("a\nb\nc\nd\n")); err != nil {
		t.Fatal(err)
	}

	entry, ok := idx.ApproxLineToOffset(4)
	if !ok {
		t.Fatal("expected entry")
	}
	if entry != (Entry{Line: 3, Offset: 4}) {
		t.Fatalf("entry = %#v, want line 3 offset 4", entry)
	}
}

func TestApproxOffsetToLine(t *testing.T) {
	idx := New(2)
	if err := idx.Build(context.Background(), strings.NewReader("aa\nbb\ncc\ndd\n")); err != nil {
		t.Fatal(err)
	}

	entry, ok := idx.ApproxOffsetToLine(6)
	if !ok {
		t.Fatal("expected entry")
	}
	if entry.Line < 2 || entry.Line > 3 {
		t.Fatalf("line estimate = %d, want around 2-3", entry.Line)
	}
	if entry.Offset != 6 {
		t.Fatalf("offset = %d, want 6", entry.Offset)
	}
}

func TestApproxUsesPriorityEntries(t *testing.T) {
	idx := New(4)
	idx.AddPriorityEntry(Entry{Line: 1, Offset: 0})
	idx.AddPriorityEntry(Entry{Line: 9, Offset: 24})

	entry, ok := idx.ApproxLineToOffset(10)
	if !ok {
		t.Fatal("expected line estimate")
	}
	if entry != (Entry{Line: 9, Offset: 24}) {
		t.Fatalf("entry = %#v, want priority entry", entry)
	}

	offsetEntry, ok := idx.ApproxOffsetToLine(24)
	if !ok {
		t.Fatal("expected offset estimate")
	}
	if offsetEntry.Line != 9 {
		t.Fatalf("line = %d, want 9", offsetEntry.Line)
	}
}

func TestBuildClearsPriorityEntries(t *testing.T) {
	idx := New(2)
	idx.AddPriorityEntry(Entry{Line: 99, Offset: 999})
	if err := idx.Build(context.Background(), strings.NewReader("a\nb\nc\nd\n")); err != nil {
		t.Fatal(err)
	}

	entry, ok := idx.ApproxLineToOffset(100)
	if !ok {
		t.Fatal("expected entry")
	}
	if entry == (Entry{Line: 99, Offset: 999}) {
		t.Fatal("priority entry should not survive completed exact index")
	}
}

func TestBuildWithEncodingUTF16LE(t *testing.T) {
	idx := New(2)
	data := []byte{
		0x61, 0x00, 0x0D, 0x00, 0x0A, 0x00,
		0x62, 0x00, 0x0D, 0x00, 0x0A, 0x00,
		0x63, 0x00, 0x0D, 0x00, 0x0A, 0x00,
		0x64, 0x00, 0x0D, 0x00, 0x0A, 0x00,
	}
	if err := idx.BuildWithEncoding(context.Background(), bytes.NewReader(data), "UTF-16LE"); err != nil {
		t.Fatal(err)
	}

	entries := idx.Entries()
	if len(entries) != 3 {
		t.Fatalf("entries = %#v, want 3 entries", entries)
	}
	if entries[1] != (Entry{Line: 3, Offset: 12}) {
		t.Fatalf("second entry = %#v, want line 3 offset 12", entries[1])
	}
	if entries[2] != (Entry{Line: 5, Offset: 24}) {
		t.Fatalf("third entry = %#v, want line 5 offset 24", entries[2])
	}
}

func TestScanLineBreaksScansDataWithoutCarryCopy(t *testing.T) {
	var offsets []int64
	carry := scanLineBreaks("UTF-8", nil, []byte("a\nb\rc\r\n"), 100, func(nextOffset int64) bool {
		offsets = append(offsets, nextOffset)
		return true
	})

	if len(carry) != 0 {
		t.Fatalf("carry length = %d, want 0 for UTF-8", len(carry))
	}
	want := []int64{102, 104, 107}
	if !equalInt64s(offsets, want) {
		t.Fatalf("offsets = %#v, want %#v", offsets, want)
	}
}

func TestScanLineBreaksUTF16LESplitAcrossCarry(t *testing.T) {
	carry := []byte{0x0A}
	var offsets []int64
	carry = scanLineBreaks("UTF-16LE", carry, []byte{0x00, 0x62, 0x00}, 11, func(nextOffset int64) bool {
		offsets = append(offsets, nextOffset)
		return true
	})

	want := []int64{12}
	if !equalInt64s(offsets, want) {
		t.Fatalf("offsets = %#v, want %#v", offsets, want)
	}
	if len(carry) != 1 || carry[0] != 0x00 {
		t.Fatalf("carry = %#v, want last byte 0x00", carry)
	}
}

func TestScanLineBreaksUTF16BESplitAcrossCarry(t *testing.T) {
	carry := []byte{0x00}
	var offsets []int64
	carry = scanLineBreaks("UTF-16BE", carry, []byte{0x0A, 0x00, 0x62}, 21, func(nextOffset int64) bool {
		offsets = append(offsets, nextOffset)
		return true
	})

	want := []int64{22}
	if !equalInt64s(offsets, want) {
		t.Fatalf("offsets = %#v, want %#v", offsets, want)
	}
	if len(carry) != 1 || carry[0] != 0x62 {
		t.Fatalf("carry = %#v, want last byte 0x62", carry)
	}
}

func equalInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
