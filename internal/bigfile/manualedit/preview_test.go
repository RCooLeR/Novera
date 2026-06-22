package manualedit

import (
	"testing"

	"novera/internal/bigfile/document"
)

func TestBuildVisiblePreviewsInsertAndReplace(t *testing.T) {
	lines := []document.VisualLine{
		{Offset: 0, DisplayEndOffset: 5, Text: "alpha"},
		{Offset: 6, DisplayEndOffset: 10, Text: "beta"},
	}
	edits := []Edit{
		{Start: 2, End: 2, Text: []byte("XY")},
		{Start: 6, End: 10, Text: []byte("omega")},
	}

	got := BuildVisiblePreviews(lines, edits, 1, 3, 32)
	if len(got) != 2 {
		t.Fatalf("BuildVisiblePreviews() len = %d, want 2", len(got))
	}
	if got[0].Kind != PreviewInsert || got[0].LineIndex != 0 || got[0].Lines[0] != "Insert: XY" {
		t.Fatalf("first preview = %#v", got[0])
	}
	if got[1].Kind != PreviewReplace || got[1].LineIndex != 1 || got[1].Lines[0] != "Replace: omega" || !got[1].Selected {
		t.Fatalf("second preview = %#v", got[1])
	}
}

func TestBuildVisiblePreviewsDeleteAndMultilineClip(t *testing.T) {
	lines := []document.VisualLine{
		{Offset: 100, DisplayEndOffset: 110, Text: "first"},
	}
	edits := []Edit{
		{Start: 100, End: 104, Text: nil},
		{Start: 108, End: 108, Text: []byte("one\ntwo\nthree\nfour")},
	}

	got := BuildVisiblePreviews(lines, edits, -1, 2, 5)
	if len(got) != 2 {
		t.Fatalf("BuildVisiblePreviews() len = %d, want 2", len(got))
	}
	if got[0].Lines[0] != "Delete staged text" {
		t.Fatalf("delete preview = %#v", got[0])
	}
	if len(got[1].Lines) != 3 {
		t.Fatalf("multiline preview lines = %#v", got[1].Lines)
	}
	if got[1].Lines[0] != "Insert: one" || got[1].Lines[1] != "  two" || got[1].Lines[2] != "Insert ..." {
		t.Fatalf("multiline preview = %#v", got[1].Lines)
	}
}

func TestBuildVisiblePreviewsWithSourceMappedEditsAfterShift(t *testing.T) {
	session := NewSession(int64(len("abcdef")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 0, End: 0, Text: []byte("XXX")}); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 4, End: 5, Text: []byte("Y")}); err != nil {
		t.Fatal(err)
	}
	lines := []document.VisualLine{
		{Offset: 0, DisplayEndOffset: 3, Text: "abc"},
	}

	got := BuildVisiblePreviews(lines, session.SourceMappedActiveEdits(), 1, 3, 32)
	if len(got) != 2 {
		t.Fatalf("BuildVisiblePreviews() len = %d, want 2: %#v", len(got), got)
	}
	if got[1].LineIndex != 0 || got[1].Lines[0] != "Replace: Y" || !got[1].Selected {
		t.Fatalf("shifted preview = %#v", got[1])
	}
}
