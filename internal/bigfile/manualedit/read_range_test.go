package manualedit

import (
	"io"
	"testing"
)

type memSrc []byte

func (m memSrc) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m)) {
		return 0, io.EOF
	}
	n := copy(p, m[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m memSrc) Size() int64 { return int64(len(m)) }

func readAll(t *testing.T, s *Session, src memSrc) string {
	t.Helper()
	b, err := s.ReadRange(src, 0, s.Size())
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	return string(b)
}

func TestSessionReadRangeReflectsEdits(t *testing.T) {
	src := memSrc("hello world")
	s := NewSession(int64(len(src)), 0)

	// length-preserving replace: "world" -> "WORLD"
	if err := s.ApplyEdit(Edit{Start: 6, End: 11, Text: []byte("WORLD")}); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s, src); got != "hello WORLD" {
		t.Fatalf("after replace = %q", got)
	}

	// sub-range reads of the edited view
	if b, _ := s.ReadRange(src, 6, 11); string(b) != "WORLD" {
		t.Fatalf("sub-range = %q", string(b))
	}
	if b, _ := s.ReadRange(src, 0, 5); string(b) != "hello" {
		t.Fatalf("sub-range = %q", string(b))
	}
}

func TestSessionReadRangeInsertAndDelete(t *testing.T) {
	src := memSrc("hello world")
	s := NewSession(int64(len(src)), 0)

	// insertion (length-changing): insert " big" at offset 5
	if err := s.ApplyEdit(Edit{Start: 5, End: 5, Text: []byte(" big")}); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s, src); got != "hello big world" {
		t.Fatalf("after insert = %q", got)
	}
	if s.Size() != 15 {
		t.Fatalf("size = %d", s.Size())
	}

	// deletion on top of the insert: remove "hello " (transformed [0,6))
	if err := s.ApplyEdit(Edit{Start: 0, End: 6, Text: nil}); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s, src); got != "big world" {
		t.Fatalf("after delete = %q", got)
	}
}

func TestSessionReadRangeNoEditsMatchesSource(t *testing.T) {
	src := memSrc("the quick brown fox")
	s := NewSession(int64(len(src)), 0)
	if got := readAll(t, s, src); got != string(src) {
		t.Fatalf("no-edit view = %q", got)
	}
}
