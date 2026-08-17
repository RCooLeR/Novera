package manualedit

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestSessionApplyUndoRedo(t *testing.T) {
	session := NewSession(int64(len("hello world")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 5, End: 5, Text: []byte(" brave")}); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 0, End: 5, Text: []byte("hi")}); err != nil {
		t.Fatal(err)
	}
	if session.EditCount() != 2 {
		t.Fatalf("edit count = %d, want 2", session.EditCount())
	}

	var out nopSyncBuffer
	if _, err := session.table.WriteTo(context.Background(), memReaderAtSize{data: []byte("hello world")}, &out, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hi brave world" {
		t.Fatalf("out = %q", out.String())
	}

	if err := session.Undo(); err != nil {
		t.Fatal(err)
	}
	if !session.CanRedo() {
		t.Fatal("expected redo after undo")
	}
	out.Reset()
	if _, err := session.table.WriteTo(context.Background(), memReaderAtSize{data: []byte("hello world")}, &out, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello brave world" {
		t.Fatalf("out after undo = %q", out.String())
	}

	if err := session.Redo(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if _, err := session.table.WriteTo(context.Background(), memReaderAtSize{data: []byte("hello world")}, &out, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hi brave world" {
		t.Fatalf("out after redo = %q", out.String())
	}
}

func TestSessionApplyAfterUndoDropsRedoHistory(t *testing.T) {
	session := NewSession(int64(len("abc")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 3, End: 3, Text: []byte("d")}); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 4, End: 4, Text: []byte("e")}); err != nil {
		t.Fatal(err)
	}
	if err := session.Undo(); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 4, End: 4, Text: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if session.CanRedo() {
		t.Fatal("redo history should be dropped after new edit")
	}

	var out bytes.Buffer
	buf := nopSyncBuffer{Buffer: out}
	if _, err := session.table.WriteTo(context.Background(), memReaderAtSize{data: []byte("abc")}, &buf, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "abcdx" {
		t.Fatalf("out = %q", buf.String())
	}
}

func TestSessionApplyEditFailureDoesNotMutateHistory(t *testing.T) {
	session := NewSession(int64(len("abc")), 4)
	if err := session.ApplyEdit(Edit{Start: 1, End: 1, Text: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	err := session.ApplyEdit(Edit{Start: 2, End: 2, Text: []byte("too-large")})
	if !errors.Is(err, ErrInsertedTextTooLarge) {
		t.Fatalf("err = %v, want ErrInsertedTextTooLarge", err)
	}
	if session.EditCount() != 1 {
		t.Fatalf("edit count after failed apply = %d, want 1", session.EditCount())
	}
	if session.CanRedo() {
		t.Fatal("failed apply should not create redo history")
	}
	if got := renderSessionForTest(t, session, "abc"); got != "axbc" {
		t.Fatalf("session output after failed apply = %q, want %q", got, "axbc")
	}

	if err := session.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := renderSessionForTest(t, session, "abc"); got != "abc" {
		t.Fatalf("session output after undo = %q, want %q", got, "abc")
	}
	if err := session.Redo(); err != nil {
		t.Fatal(err)
	}
	if got := renderSessionForTest(t, session, "abc"); got != "axbc" {
		t.Fatalf("session output after redo = %q, want %q", got, "axbc")
	}
}

func TestSessionFailedApplyAfterUndoPreservesRedoHistory(t *testing.T) {
	session := NewSession(int64(len("abc")), 4)
	if err := session.ApplyEdit(Edit{Start: 3, End: 3, Text: []byte("d")}); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 4, End: 4, Text: []byte("e")}); err != nil {
		t.Fatal(err)
	}
	if err := session.Undo(); err != nil {
		t.Fatal(err)
	}

	err := session.ApplyEdit(Edit{Start: 4, End: 4, Text: []byte("too-large")})
	if !errors.Is(err, ErrInsertedTextTooLarge) {
		t.Fatalf("err = %v, want ErrInsertedTextTooLarge", err)
	}
	if session.EditCount() != 1 {
		t.Fatalf("edit count after failed branch apply = %d, want 1", session.EditCount())
	}
	if !session.CanRedo() {
		t.Fatal("failed branch apply should preserve redo history")
	}
	if got := renderSessionForTest(t, session, "abc"); got != "abcd" {
		t.Fatalf("session output after failed branch apply = %q, want %q", got, "abcd")
	}

	if err := session.Redo(); err != nil {
		t.Fatal(err)
	}
	if got := renderSessionForTest(t, session, "abc"); got != "abcde" {
		t.Fatalf("session output after redo = %q, want %q", got, "abcde")
	}
}

func TestSourceMappedActiveEditsAccountsForPriorInsert(t *testing.T) {
	session := NewSession(int64(len("abcdef")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 0, End: 0, Text: []byte("XXX")}); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 4, End: 5, Text: []byte("Y")}); err != nil {
		t.Fatal(err)
	}

	got := session.SourceMappedActiveEdits()
	if len(got) != 2 {
		t.Fatalf("mapped edits len = %d, want 2", len(got))
	}
	if got[0].Start != 0 || got[0].End != 0 {
		t.Fatalf("first mapped edit = %#v, want source insert at 0", got[0])
	}
	if got[1].Start != 1 || got[1].End != 2 {
		t.Fatalf("second mapped edit = %#v, want source range 1..2", got[1])
	}
}

func TestSourceMappedActiveEditsAccountsForPriorDelete(t *testing.T) {
	session := NewSession(int64(len("abcdef")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 0, End: 2, Text: nil}); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 1, End: 2, Text: []byte("Y")}); err != nil {
		t.Fatal(err)
	}

	got := session.SourceMappedActiveEdits()
	if len(got) != 2 {
		t.Fatalf("mapped edits len = %d, want 2", len(got))
	}
	if got[0].Start != 0 || got[0].End != 2 {
		t.Fatalf("first mapped edit = %#v, want source range 0..2", got[0])
	}
	if got[1].Start != 3 || got[1].End != 4 {
		t.Fatalf("second mapped edit = %#v, want source range 3..4", got[1])
	}
}

func TestSourceMappedModifiedRangesAreSortedSourceAnchors(t *testing.T) {
	session := NewSession(int64(len("abcdef")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 0, End: 0, Text: []byte("XXX")}); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 4, End: 5, Text: []byte("Y")}); err != nil {
		t.Fatal(err)
	}

	got := session.SourceMappedModifiedRanges()
	if len(got) != 2 {
		t.Fatalf("mapped ranges len = %d, want 2: %#v", len(got), got)
	}
	if got[0] != (Range{Start: 0, End: 0}) || got[1] != (Range{Start: 1, End: 2}) {
		t.Fatalf("mapped ranges = %#v", got)
	}
}

func TestSourceRangeToTransformedAccountsForPriorInsert(t *testing.T) {
	session := NewSession(int64(len("abcdef")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 0, End: 0, Text: []byte("XXX")}); err != nil {
		t.Fatal(err)
	}

	got, ok := session.SourceRangeToTransformed(3, 4)
	if !ok {
		t.Fatal("SourceRangeToTransformed returned !ok")
	}
	if got != (Range{Start: 6, End: 7}) {
		t.Fatalf("transformed range = %#v, want 6..7", got)
	}
}

func TestSourceRangeToTransformedAccountsForPriorDelete(t *testing.T) {
	session := NewSession(int64(len("abcdef")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 0, End: 2, Text: nil}); err != nil {
		t.Fatal(err)
	}

	got, ok := session.SourceRangeToTransformed(3, 4)
	if !ok {
		t.Fatal("SourceRangeToTransformed returned !ok")
	}
	if got != (Range{Start: 1, End: 2}) {
		t.Fatalf("transformed range = %#v, want 1..2", got)
	}
}

func TestSourceRangeToTransformedRejectsReplacedSourceRange(t *testing.T) {
	session := NewSession(int64(len("abcdef")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 1, End: 3, Text: []byte("XX")}); err != nil {
		t.Fatal(err)
	}

	if _, ok := session.SourceRangeToTransformed(1, 3); ok {
		t.Fatal("SourceRangeToTransformed returned ok for already replaced source range")
	}
}

func TestSessionRevisionIsMonotonicAndExhaustionIsTransactional(t *testing.T) {
	session := NewSession(6, DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 1, End: 2, Text: []byte("X")}); err != nil {
		t.Fatal(err)
	}
	if session.Revision() != 1 {
		t.Fatalf("revision after apply = %d", session.Revision())
	}
	if err := session.Undo(); err != nil {
		t.Fatal(err)
	}
	if session.Revision() != 2 {
		t.Fatalf("revision after undo = %d", session.Revision())
	}
	if err := session.Redo(); err != nil {
		t.Fatal(err)
	}
	if session.Revision() != 3 {
		t.Fatalf("revision after redo = %d", session.Revision())
	}
	if err := session.DiscardEdits(); err != nil {
		t.Fatal(err)
	}
	if session.Revision() != 4 || session.HasEdits() {
		t.Fatalf("discard state revision=%d edits=%v", session.Revision(), session.HasEdits())
	}

	session.revision = ^uint64(0)
	sizeBefore := session.Size()
	if err := session.ApplyEdit(Edit{Start: 0, End: 0, Text: []byte("!")}); !errors.Is(err, ErrSessionRevisionExhausted) {
		t.Fatalf("exhausted apply error = %v", err)
	}
	if session.Size() != sizeBefore || session.HasEdits() || session.Revision() != ^uint64(0) {
		t.Fatal("revision exhaustion mutated session state")
	}
	if err := session.DiscardEdits(); !errors.Is(err, ErrSessionRevisionExhausted) {
		t.Fatalf("exhausted discard error = %v", err)
	}
}

func TestTransformedRangeToSourceAccountsForInsertAndDelete(t *testing.T) {
	t.Run("insert", func(t *testing.T) {
		session := NewSession(6, DefaultMaxInsertedBytes)
		if err := session.ApplyEdit(Edit{Start: 2, End: 2, Text: []byte("XY")}); err != nil {
			t.Fatal(err)
		}
		inserted, ok := session.TransformedRangeToSource(2, 4)
		if !ok || inserted != (Range{Start: 2, End: 2}) {
			t.Fatalf("inserted mapping = %+v, %v", inserted, ok)
		}
		following, ok := session.TransformedRangeToSource(4, 6)
		if !ok || following != (Range{Start: 2, End: 4}) {
			t.Fatalf("following mapping = %+v, %v", following, ok)
		}
	})

	t.Run("delete", func(t *testing.T) {
		session := NewSession(6, DefaultMaxInsertedBytes)
		if err := session.ApplyEdit(Edit{Start: 2, End: 4}); err != nil {
			t.Fatal(err)
		}
		deleted, ok := session.TransformedRangeToSource(2, 2)
		if !ok || deleted != (Range{Start: 2, End: 4}) {
			t.Fatalf("deleted mapping = %+v, %v", deleted, ok)
		}
		following, ok := session.TransformedRangeToSource(2, 4)
		if !ok || following != (Range{Start: 4, End: 6}) {
			t.Fatalf("following mapping = %+v, %v", following, ok)
		}
	})
}

func renderSessionForTest(t *testing.T, session *Session, source string) string {
	t.Helper()

	var out nopSyncBuffer
	if _, err := session.table.WriteTo(context.Background(), memReaderAtSize{data: []byte(source)}, &out, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
