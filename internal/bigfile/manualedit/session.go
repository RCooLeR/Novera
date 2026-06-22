package manualedit

import (
	"errors"

	"novera/internal/bigfile/document"
)

// Session is a single-owner mutable staging model. Callers must confine it to
// one goroutine/UI owner or provide external synchronization before mutating it.
type Session struct {
	originalSize int64
	maxInserted  int64
	history      []Edit
	cursor       int
	table        *PieceTable
}

func NewSession(size int64, maxInsertedBytes int64) *Session {
	if maxInsertedBytes <= 0 {
		maxInsertedBytes = DefaultMaxInsertedBytes
	}
	s := &Session{
		originalSize: size,
		maxInserted:  maxInsertedBytes,
	}
	s.table = NewPieceTable(size)
	s.table.SetMaxInsertedBytes(maxInsertedBytes)
	return s
}

func (s *Session) ApplyEdit(edit Edit) error {
	if s == nil {
		return errors.New("session is required")
	}

	nextHistory := make([]Edit, 0, s.cursor+1)
	if s.cursor < len(s.history) {
		nextHistory = append(nextHistory, s.history[:s.cursor]...)
	} else {
		nextHistory = append(nextHistory, s.history...)
	}
	nextHistory = append(nextHistory, cloneEdit(edit))

	table, err := s.buildTable(len(nextHistory), nextHistory)
	if err != nil {
		return err
	}

	s.history = nextHistory
	s.cursor = len(nextHistory)
	s.table = table
	return nil
}

func (s *Session) Undo() error {
	if s == nil {
		return errors.New("session is required")
	}
	if !s.CanUndo() {
		return nil
	}
	nextCursor := s.cursor - 1
	table, err := s.buildTable(nextCursor, s.history)
	if err != nil {
		return err
	}
	s.cursor = nextCursor
	s.table = table
	return nil
}

func (s *Session) Redo() error {
	if s == nil {
		return errors.New("session is required")
	}
	if !s.CanRedo() {
		return nil
	}
	nextCursor := s.cursor + 1
	table, err := s.buildTable(nextCursor, s.history)
	if err != nil {
		return err
	}
	s.cursor = nextCursor
	s.table = table
	return nil
}

func (s *Session) CanUndo() bool {
	return s != nil && s.cursor > 0
}

func (s *Session) CanRedo() bool {
	return s != nil && s.cursor < len(s.history)
}

func (s *Session) EditCount() int {
	if s == nil {
		return 0
	}
	return s.cursor
}

func (s *Session) ModifiedRanges() []Range {
	if s == nil || s.table == nil {
		return nil
	}
	return s.table.ModifiedRanges()
}

func (s *Session) ActiveEdits() []Edit {
	if s == nil || s.cursor == 0 {
		return nil
	}
	out := make([]Edit, 0, s.cursor)
	for i := 0; i < s.cursor; i++ {
		out = append(out, cloneEdit(s.history[i]))
	}
	return out
}

// SourceMappedActiveEdits returns active edits anchored to original source offsets for viewport rendering.
func (s *Session) SourceMappedActiveEdits() []Edit {
	if s == nil || s.cursor == 0 {
		return nil
	}
	table := NewPieceTable(s.originalSize)
	table.SetMaxInsertedBytes(s.maxInserted)
	out := make([]Edit, 0, s.cursor)
	for i := 0; i < s.cursor; i++ {
		edit := cloneEdit(s.history[i])
		sourceRange := table.sourceRangeForTransformedRange(edit.Start, edit.End)
		out = append(out, Edit{
			Start: sourceRange.Start,
			End:   sourceRange.End,
			Text:  append([]byte(nil), edit.Text...),
		})
		if err := table.Replace(edit.Start, edit.End, edit.Text); err != nil {
			return out
		}
	}
	return out
}

// SourceMappedModifiedRanges returns source-offset ranges suitable for overview and navigation markers.
func (s *Session) SourceMappedModifiedRanges() []Range {
	edits := s.SourceMappedActiveEdits()
	if len(edits) == 0 {
		return nil
	}
	table := &PieceTable{}
	for _, edit := range edits {
		table.recordModifiedRange(edit.Start, edit.End)
	}
	return table.ModifiedRanges()
}

// SourceRangeToTransformed maps an original source byte range into the current
// transformed session coordinate space. Callers that stage source-anchored
// patches after earlier edits must use this before ApplyEdit.
func (s *Session) SourceRangeToTransformed(start int64, end int64) (Range, bool) {
	if s == nil || s.table == nil {
		return Range{}, false
	}
	return s.table.sourceRangeToTransformedRange(start, end)
}

func (s *Session) Size() int64 {
	if s == nil || s.table == nil {
		return 0
	}
	return s.table.Size()
}

// OriginalSize is the source document's byte size (before edits).
func (s *Session) OriginalSize() int64 {
	if s == nil {
		return 0
	}
	return s.originalSize
}

// ReadRange returns the transformed (edited) bytes in [start, end), reading
// unedited spans from src. Used to render a window reflecting staged edits.
func (s *Session) ReadRange(src document.ReaderAtSize, start int64, end int64) ([]byte, error) {
	if s == nil || s.table == nil {
		return nil, errors.New("session is required")
	}
	return s.table.ReadRange(src, start, end)
}

func (s *Session) HasEdits() bool {
	return s != nil && s.cursor > 0
}

func (s *Session) buildTable(cursor int, history []Edit) (*PieceTable, error) {
	table := NewPieceTable(s.originalSize)
	table.SetMaxInsertedBytes(s.maxInserted)
	for i := 0; i < cursor; i++ {
		if err := table.Replace(history[i].Start, history[i].End, history[i].Text); err != nil {
			return nil, err
		}
	}
	return table, nil
}

func cloneEdit(edit Edit) Edit {
	out := edit
	if edit.Text != nil {
		out.Text = append([]byte(nil), edit.Text...)
	}
	return out
}
