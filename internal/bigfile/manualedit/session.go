package manualedit

import (
	"context"
	"errors"
	"fmt"
	"io"

	"novera/internal/bigfile/document"
)

// Session is a single-owner mutable staging model. Callers must confine it to
// one goroutine/UI owner or provide external synchronization before mutating it.
type Session struct {
	originalSize int64
	limits       Limits
	history      []Edit
	cursor       int
	revision     uint64
	table        *PieceTable
	binding      *sourceBinding
}

func NewSession(size int64, maxInsertedBytes int64) *Session {
	session, err := NewSessionWithLimits(size, legacyLimits(maxInsertedBytes))
	if err != nil {
		// legacyLimits always returns a complete positive policy. Preserve this
		// compatibility constructor's no-error signature without constructing a
		// piece table with an invalid negative source size.
		size = max64(size, 0)
		limits := DefaultLimits()
		table := NewPieceTable(size)
		table.setLimits(limits)
		return &Session{originalSize: size, limits: limits, table: table}
	}
	return session
}

func NewSessionWithLimits(size int64, limits Limits) (*Session, error) {
	if size < 0 {
		return nil, errors.New("source size must be non-negative")
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	s := &Session{originalSize: size, limits: limits}
	s.table = NewPieceTable(size)
	s.table.setLimits(limits)
	return s, nil
}

func (s *Session) ApplyEdit(edit Edit) error {
	if s == nil {
		return errors.New("session is required")
	}
	if s.revision == ^uint64(0) {
		return ErrSessionRevisionExhausted
	}
	if err := s.table.validateReplace(edit.Start, edit.End, edit.Text); err != nil {
		return err
	}

	prefix := s.cursor
	nextDepth := prefix + 1
	if nextDepth > s.limits.MaxEditCount {
		return ErrEditCountLimit
	}
	if nextDepth > s.limits.MaxHistoryDepth {
		return ErrHistoryDepthLimit
	}

	historyBytes, err := historyBytesFor(s.history[:prefix])
	if err != nil {
		return err
	}
	editBytes, err := historyEditBytes(edit)
	if err != nil {
		return err
	}
	historyBytes, err = checkedMemorySum(historyBytes, editBytes)
	if err != nil || historyBytes > s.limits.MaxHistoryBytes {
		return ErrHistoryBytesLimit
	}
	if err := s.preflightApply(prefix, edit, historyBytes); err != nil {
		return err
	}

	// Allocate only after every deterministic count/byte/transient check. The
	// caller-owned text is cloned only after the rebuilt table succeeds.
	nextHistory := make([]Edit, 0, nextDepth)
	nextHistory = append(nextHistory, s.history[:prefix]...)
	nextHistory = append(nextHistory, edit)

	table, err := s.buildTable(len(nextHistory), nextHistory)
	if err != nil {
		return err
	}

	nextHistory[len(nextHistory)-1] = cloneEdit(edit)
	s.history = nextHistory
	s.cursor = len(nextHistory)
	s.table = table
	s.revision++
	return nil
}

func (s *Session) Undo() error {
	if s == nil {
		return errors.New("session is required")
	}
	if !s.CanUndo() {
		return nil
	}
	if s.revision == ^uint64(0) {
		return ErrSessionRevisionExhausted
	}
	nextCursor := s.cursor - 1
	historyBytes, err := historyBytesFor(s.history)
	if err != nil {
		return err
	}
	if err := s.preflightRebuild(nextCursor, s.history, historyBytes); err != nil {
		return err
	}
	table, err := s.buildTable(nextCursor, s.history)
	if err != nil {
		return err
	}
	s.cursor = nextCursor
	s.table = table
	s.revision++
	return nil
}

func (s *Session) Redo() error {
	if s == nil {
		return errors.New("session is required")
	}
	if !s.CanRedo() {
		return nil
	}
	if s.revision == ^uint64(0) {
		return ErrSessionRevisionExhausted
	}
	nextCursor := s.cursor + 1
	historyBytes, err := historyBytesFor(s.history)
	if err != nil {
		return err
	}
	if err := s.preflightRebuild(nextCursor, s.history, historyBytes); err != nil {
		return err
	}
	table, err := s.buildTable(nextCursor, s.history)
	if err != nil {
		return err
	}
	s.cursor = nextCursor
	s.table = table
	s.revision++
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

// Revision is a monotonic identity for staged state. It changes after every
// successful Apply, Undo, Redo, or Discard operation.
func (s *Session) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
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
	table.setLimits(s.limits)
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
			return nil
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

// TransformedRangeToSource maps an edited-view range to its conservative
// original-source span. Callers must bound the returned source span before
// reading it.
func (s *Session) TransformedRangeToSource(start int64, end int64) (Range, bool) {
	if s == nil || s.table == nil {
		return Range{}, false
	}
	return s.table.transformedRangeToSourceRange(start, end)
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

// DiscardEdits clears staged history while retaining the exact source binding
// prepared for this session.
func (s *Session) DiscardEdits() error {
	if s == nil {
		return errors.New("session is required")
	}
	if s.revision == ^uint64(0) {
		return ErrSessionRevisionExhausted
	}
	s.history = nil
	s.cursor = 0
	s.table = NewPieceTable(s.originalSize)
	s.table.setLimits(s.limits)
	s.revision++
	return nil
}

// WriteTo streams the immutable current staging snapshot against src. The
// caller owns output synchronization/publication; this method verifies that
// the exact edited byte count was produced.
func (s *Session) WriteTo(ctx context.Context, src document.ReaderAtSize, dst io.Writer, progress func(Progress)) (int64, error) {
	if s == nil || s.table == nil {
		return 0, errors.New("session is required")
	}
	if src == nil || src.Size() != s.originalSize {
		return 0, fmt.Errorf("staged source size changed from %d", s.originalSize)
	}
	if dst == nil {
		return 0, errors.New("destination is required")
	}
	written, err := s.table.WriteTo(ctx, src, noSyncWriter{Writer: dst}, WriteOptions{Progress: progress})
	if err != nil {
		return written, err
	}
	if written != s.table.Size() {
		return written, fmt.Errorf("manual-edit output incomplete: wrote %d of %d bytes", written, s.table.Size())
	}
	return written, nil
}

type noSyncWriter struct {
	io.Writer
}

func (noSyncWriter) Sync() error { return nil }

func (s *Session) buildTable(cursor int, history []Edit) (*PieceTable, error) {
	table := NewPieceTable(s.originalSize)
	table.setLimits(s.limits)
	for i := 0; i < cursor; i++ {
		if err := table.Replace(history[i].Start, history[i].End, history[i].Text); err != nil {
			return nil, err
		}
	}
	return table, nil
}

func (s *Session) preflightApply(prefix int, edit Edit, retainedHistoryBytes int64) error {
	if prefix < 0 || prefix > len(s.history) {
		return ErrTransientMemoryLimit
	}
	var activeTextBytes int64
	var err error
	for i := 0; i < prefix; i++ {
		activeTextBytes, err = checkedMemorySum(activeTextBytes, int64(len(s.history[i].Text)))
		if err != nil {
			return ErrTransientMemoryLimit
		}
	}
	activeTextBytes, err = checkedMemorySum(activeTextBytes, int64(len(edit.Text)))
	if err != nil {
		return ErrTransientMemoryLimit
	}
	return s.preflightEstimate(prefix+1, activeTextBytes, retainedHistoryBytes, prefix+1)
}

func (s *Session) preflightRebuild(cursor int, history []Edit, retainedHistoryBytes int64) error {
	if cursor < 0 || cursor > len(history) {
		return ErrTransientMemoryLimit
	}
	var activeTextBytes int64
	var err error
	for i := 0; i < cursor; i++ {
		activeTextBytes, err = checkedMemorySum(activeTextBytes, int64(len(history[i].Text)))
		if err != nil {
			return ErrTransientMemoryLimit
		}
	}
	return s.preflightEstimate(cursor, activeTextBytes, retainedHistoryBytes, len(history))
}

func (s *Session) preflightEstimate(cursor int, activeTextBytes int64, retainedHistoryBytes int64, historyEntries int) error {
	if cursor < 0 || historyEntries < 0 {
		return ErrTransientMemoryLimit
	}
	currentTableBytes, err := s.table.residentBytes()
	if err != nil {
		return ErrTransientMemoryLimit
	}
	currentHistoryBytes, err := historyBytesFor(s.history)
	if err != nil {
		return ErrTransientMemoryLimit
	}

	worstPieces := int64(1)
	if cursor > 0 {
		if int64(cursor) > (int64(maxInt())-1)/2 {
			return ErrTransientMemoryLimit
		}
		worstPieces += int64(cursor) * 2
	}
	worstPieceBytes, err := checkedMemoryProduct(worstPieces, pieceAccountingBytes)
	if err != nil {
		return ErrTransientMemoryLimit
	}
	modifiedRangeBytes, err := checkedMemoryProduct(int64(cursor), rangeAccountingBytes)
	if err != nil {
		return ErrTransientMemoryLimit
	}
	newTableBytes, err := checkedMemorySum(
		activeTextBytes,
		worstPieceBytes,
		modifiedRangeBytes,
	)
	if err != nil || newTableBytes > int64(maxInt())/4 {
		return ErrTransientMemoryLimit
	}

	// PieceTable.Replace is transactional: the retained current table, its
	// candidate pieces, normalization, and compacted added buffer can overlap.
	rebuildPeakBytes := newTableBytes * 4
	historySliceBytes, err := checkedMemoryProduct(int64(historyEntries), sliceAccountingBytes)
	if err != nil {
		return ErrTransientMemoryLimit
	}
	transient, err := checkedMemorySum(
		currentTableBytes,
		currentHistoryBytes,
		retainedHistoryBytes,
		rebuildPeakBytes,
		historySliceBytes,
		sourceWriteBufferBytes,
	)
	if err != nil || transient > s.limits.MaxTransientBytes {
		return ErrTransientMemoryLimit
	}
	return nil
}

func historyEditBytes(edit Edit) (int64, error) {
	return checkedMemorySum(historyAccountingBytes, int64(len(edit.Text)))
}

func historyBytesFor(history []Edit) (int64, error) {
	var total int64
	for _, edit := range history {
		bytes, err := historyEditBytes(edit)
		if err != nil {
			return 0, ErrHistoryBytesLimit
		}
		total, err = checkedMemorySum(total, bytes)
		if err != nil {
			return 0, ErrHistoryBytesLimit
		}
	}
	return total, nil
}

func cloneEdit(edit Edit) Edit {
	out := edit
	if edit.Text != nil {
		out.Text = append([]byte(nil), edit.Text...)
	}
	return out
}
