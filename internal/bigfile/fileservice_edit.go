package bigfile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/bigfile/manualedit"
	"novera/internal/bigfile/session"
)

// StagedEdit is one pending edit, anchored to the original source, with short
// before/after previews for the diff panel.
type StagedEdit struct {
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Line  int64  `json:"line"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

const stagedPreviewBytes = 240
const maxPreparedEditSessions = 4

var (
	ErrInPlaceSaveDisabled     = errors.New("in-place save is disabled; use Save as copy")
	ErrEditPreparationRequired = errors.New("large-file editing must be prepared before staging changes")
	ErrPreparedEditLimit       = errors.New("prepared edit session limit reached")
	ErrEditSessionDirty        = errors.New("cannot release an edit session with staged changes")
	ErrEditRequestTooLarge     = errors.New("edit request exceeds the bounded bridge limit")
	ErrFileNotEditable         = errors.New("file is not byte-exact editable UTF-8/LF text")
	ErrSaveCopyStateChanged    = errors.New("staged edits changed after save-copy approval")
	ErrDiffCoordinateMapping   = errors.New("diff window cannot map edited offsets to source offsets")
)

var saveCopySaveDialog = func() (string, error) {
	return application.Get().Dialog.SaveFile().
		SetMessage("Save edited copy as").
		PromptForSingleSelection()
}

// GetStagedEdits returns the pending edits (source-anchored) for the diff panel.
func (s *FileService) GetStagedEdits(fileID string) ([]StagedEdit, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return nil, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if f.Edit == nil || !f.Edit.HasEdits() {
		return nil, nil
	}
	edits := f.Edit.SourceMappedActiveEdits()
	out := make([]StagedEdit, 0, len(edits))
	for _, e := range edits {
		oldEnd := e.End
		if oldEnd > e.Start+stagedPreviewBytes {
			oldEnd = e.Start + stagedPreviewBytes
		}
		oldBytes, err := f.Edit.ReadSourceRangeContext(context.Background(), e.Start, oldEnd, stagedPreviewBytes)
		if err != nil {
			return nil, err
		}
		line, _ := f.Doc.ApproxOffsetToLine(e.Start)
		out = append(out, StagedEdit{
			Start: e.Start,
			End:   e.End,
			Line:  line,
			Old:   string(oldBytes),
			New:   truncateBytes(e.Text, stagedPreviewBytes),
		})
	}
	return out, nil
}

func (s *FileService) hasPreparedEdit(fileID string) bool {
	s.editMu.Lock()
	_, ok := s.preparedEdits[fileID]
	s.editMu.Unlock()
	return ok
}

func (s *FileService) reservePreparedEdit(fileID string) error {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	if s.preparedEdits == nil {
		s.preparedEdits = make(map[string]struct{})
	}
	if _, ok := s.preparedEdits[fileID]; ok {
		return nil
	}
	if len(s.preparedEdits) >= maxPreparedEditSessions {
		return fmt.Errorf("%w (%d)", ErrPreparedEditLimit, maxPreparedEditSessions)
	}
	s.preparedEdits[fileID] = struct{}{}
	return nil
}

func (s *FileService) releasePreparedEdit(fileID string) {
	s.editMu.Lock()
	delete(s.preparedEdits, fileID)
	s.editMu.Unlock()
}

// PrepareEditSession performs the one exact full-source fingerprint as a
// cancellable, progress-reporting job before the frontend enables edit mode.
// StageEdit never performs this whole-file pass.
func (s *FileService) PrepareEditSession(fileID string) (StagingState, error) {
	if err := s.ensureServiceRunning(); err != nil {
		return StagingState{}, err
	}
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return StagingState{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if err := requireEditableFile(f); err != nil {
		return StagingState{}, err
	}
	if f.Edit != nil {
		if !s.hasPreparedEdit(fileID) || !f.Edit.MatchesSourceGeneration(f.Path, f.Generation) {
			return StagingState{}, ErrEditPreparationRequired
		}
		return stagingState(f), nil
	}
	if s.hasPreparedEdit(fileID) {
		s.releasePreparedEdit(fileID)
	}
	if err := s.reservePreparedEdit(fileID); err != nil {
		return StagingState{}, err
	}
	keepReservation := false
	defer func() {
		if !keepReservation {
			s.releasePreparedEdit(fileID)
		}
	}()

	state, err := runServiceJob(s, jobSpec{
		Title:  "Prepare editing",
		Kind:   jobKindSourceVerification,
		FileID: fileID,
		Total:  f.Doc.Size(),
	}, func(ctx context.Context, report func(int64, int64, string)) (StagingState, error) {
		candidate, err := captureExactEditSession(ctx, f, func(completed, total int64) {
			report(completed, total, "verifying source bytes")
		})
		if err != nil {
			return StagingState{}, err
		}
		if err := ctx.Err(); err != nil {
			return StagingState{}, err
		}
		jobID, ok := serviceJobID(ctx)
		if !ok {
			return StagingState{}, errors.New("prepare-edit job identity is unavailable")
		}
		var installErr error
		installed := s.jobs().commit(jobID, func() bool {
			_, installErr = f.InstallEditSession(candidate)
			return installErr == nil
		})
		if installErr != nil {
			return StagingState{}, installErr
		}
		if !installed {
			if err := ctx.Err(); err != nil {
				return StagingState{}, err
			}
			return StagingState{}, ErrJobCancelled
		}
		return stagingState(f), nil
	})
	if err != nil {
		return StagingState{}, err
	}
	keepReservation = true
	return state, nil
}

// ReleaseCleanEditSession drops the retained exact fingerprint only when no
// staged edits exist. Dirty state is never discarded implicitly.
func (s *FileService) ReleaseCleanEditSession(fileID string) (StagingState, error) {
	if err := s.ensureServiceRunning(); err != nil {
		return StagingState{}, err
	}
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return StagingState{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if f.Edit != nil && f.Edit.HasEdits() {
		return stagingState(f), ErrEditSessionDirty
	}
	f.ResetEdits()
	s.releasePreparedEdit(fileID)
	return stagingState(f), nil
}

// SaveCopyViaDialog shows a native save dialog and writes the edited copy there.
// A cancelled dialog returns an empty SaveResult (Mode == "") with nil error.
func (s *FileService) SaveCopyViaDialog(fileID string) (SaveResult, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return SaveResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	approval, err := captureSaveCopyApproval(f)
	f.Release()
	if err != nil {
		return SaveResult{}, err
	}
	dst, err := saveCopySaveDialog()
	if err != nil {
		return SaveResult{}, err
	}
	if strings.TrimSpace(dst) == "" {
		return SaveResult{}, nil // cancelled
	}
	return s.saveCopyApproved(fileID, dst, approval)
}

type saveCopyApproval struct {
	generation uint64
	edit       *manualedit.Session
	revision   uint64
}

func captureSaveCopyApproval(file *session.File) (saveCopyApproval, error) {
	if file == nil || file.Edit == nil || !file.Edit.HasEdits() {
		return saveCopyApproval{}, errors.New("no staged edits")
	}
	return saveCopyApproval{
		generation: file.Generation,
		edit:       file.Edit,
		revision:   file.Edit.Revision(),
	}, nil
}

func (approval saveCopyApproval) matches(file *session.File) bool {
	return file != nil && approval.generation != 0 && approval.edit != nil &&
		file.Generation == approval.generation && file.Edit == approval.edit &&
		file.Edit.HasEdits() && file.Edit.Revision() == approval.revision
}

func truncateBytes(b []byte, max int) string {
	if len(b) > max {
		return string(b[:max])
	}
	return string(b)
}

const editWindowBytes = 1 << 20 // 1 MiB editable window budget

// StagingState summarizes pending edits and which save modes are available.
type StagingState struct {
	EditCount        int   `json:"editCount"`
	OriginalSize     int64 `json:"originalSize"`
	EditedSize       int64 `json:"editedSize"`
	NetDelta         int64 `json:"netDelta"`
	LengthPreserving bool  `json:"lengthPreserving"`
	InPlaceEligible  bool  `json:"inPlaceEligible"`
}

// SaveResult describes a completed save.
type SaveResult struct {
	Mode         string `json:"mode"` // "patch" | "copy"
	BytesWritten int64  `json:"bytesWritten"`
	OutputPath   string `json:"outputPath"`
}

// editableEncoding reports whether a file can be edited in v1: byte-exact
// round-tripping is only guaranteed for UTF-8/ASCII with LF line endings.
func editableEncoding(encoding, lineEnding string) bool {
	enc := strings.ToLower(strings.TrimSpace(encoding))
	utf8ish := enc == "utf-8" || enc == "utf8" || enc == "ascii" || enc == ""
	le := strings.ToLower(strings.TrimSpace(lineEnding))
	return utf8ish && (le == "lf" || le == "none" || le == "unknown" || le == "")
}

// GetEditWindow returns a byte-exact, line-aligned UTF-8 window that reflects
// staged edits (reads the edited view when edits exist, else the raw document).
// startByte is aligned down to a line start; pass any byte for go-to / prev.
func (s *FileService) GetEditWindow(fileID string, startByte int64, maxBytes int) (Window, error) {
	if startByte < 0 {
		return Window{}, fmt.Errorf("%w: start byte must not be negative", ErrEditRequestTooLarge)
	}
	if maxBytes < 0 {
		return Window{}, fmt.Errorf("%w: byte budget must not be negative", ErrEditRequestTooLarge)
	}
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return Window{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if err := requireEditableFile(f); err != nil {
		return Window{}, err
	}
	if maxBytes == 0 {
		maxBytes = editWindowBytes
	}
	if maxBytes < utf8.UTFMax {
		return Window{}, fmt.Errorf("%w: requested window %d < %d-byte UTF-8 boundary minimum", ErrEditRequestTooLarge, maxBytes, utf8.UTFMax)
	}
	if maxBytes > maxEditWindowBytes {
		return Window{}, fmt.Errorf("%w: requested window %d > %d bytes", ErrEditRequestTooLarge, maxBytes, maxEditWindowBytes)
	}

	var size int64
	var readRange func(a, b int64) ([]byte, error)
	if f.Edit != nil && f.Edit.HasEdits() {
		sess := f.Edit
		size = sess.Size()
		readRange = func(a, b int64) ([]byte, error) {
			return sess.ReadRangeContext(context.Background(), a, b, int64(maxBytes))
		}
	} else {
		size = f.Doc.Size()
		readRange = func(a, b int64) ([]byte, error) { return f.Doc.ReadRange(a, b) }
	}

	if startByte > size {
		startByte = size
	}

	aligned, err := alignToLineStart(readRange, startByte, int64(maxBytes))
	if err != nil {
		return Window{}, err
	}
	aligned, err = alignUTF8WindowStart(readRange, aligned, size)
	if err != nil {
		return Window{}, err
	}
	readEnd := boundedEditWindowEnd(aligned, size, int64(maxBytes))
	raw, err := readRange(aligned, readEnd)
	if err != nil {
		return Window{}, err
	}
	end := aligned + int64(len(raw))
	if readEnd < size {
		if nl := lastIndexByte(raw, '\n'); nl >= 0 {
			raw = raw[:nl+1]
			end = aligned + int64(nl+1)
		}
	}
	raw, trimmed, err := trimEditableUTF8Suffix(raw, end >= size, "requested window")
	if err != nil {
		return Window{}, err
	}
	end -= int64(trimmed)

	firstLine, lok := f.Doc.ApproxOffsetToLine(aligned)
	approx := true
	switch {
	case aligned == 0:
		firstLine, approx = 1, false
	case !lok || firstLine <= 0:
		firstLine = 1
	}

	offsets := make([]int64, 0, 256)
	numbers := make([]int64, 0, 256)
	lineNo := firstLine
	offsets = append(offsets, aligned)
	numbers = append(numbers, lineNo)
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			lineNo++
			offsets = append(offsets, aligned+int64(i+1))
			numbers = append(numbers, lineNo)
		}
	}

	return Window{
		FileID:      f.ID,
		StartByte:   aligned,
		NextByte:    end,
		Text:        string(raw),
		LineOffsets: offsets,
		LineNumbers: numbers,
		AtBOF:       aligned == 0,
		AtEOF:       end >= size,
		Approx:      approx,
	}, nil
}

// DiffWindow holds corresponding original and edited spans. StartByte and
// NextByte remain edited-coordinate aliases for bridge compatibility.
type DiffWindow struct {
	StartByte         int64  `json:"startByte"`
	NextByte          int64  `json:"nextByte"`
	EditedStartByte   int64  `json:"editedStartByte"`
	EditedNextByte    int64  `json:"editedNextByte"`
	OriginalStartByte int64  `json:"originalStartByte"`
	OriginalNextByte  int64  `json:"originalNextByte"`
	Original          string `json:"original"`
	Edited            string `json:"edited"`
	AtBOF             bool   `json:"atBof"`
	AtEOF             bool   `json:"atEof"`
}

// GetDiffWindow returns the edited and original text for a line-aligned window,
// for the side-by-side diff view.
func (s *FileService) GetDiffWindow(fileID string, startByte int64, maxBytes int) (DiffWindow, error) {
	if startByte < 0 {
		return DiffWindow{}, fmt.Errorf("%w: start byte must not be negative", ErrEditRequestTooLarge)
	}
	if maxBytes < 0 {
		return DiffWindow{}, fmt.Errorf("%w: byte budget must not be negative", ErrEditRequestTooLarge)
	}
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return DiffWindow{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if err := requireEditableFile(f); err != nil {
		return DiffWindow{}, err
	}
	if maxBytes == 0 {
		maxBytes = editWindowBytes
	}
	if maxBytes < utf8.UTFMax {
		return DiffWindow{}, fmt.Errorf("%w: requested diff window %d < %d-byte UTF-8 boundary minimum", ErrEditRequestTooLarge, maxBytes, utf8.UTFMax)
	}
	if maxBytes > maxEditWindowBytes {
		return DiffWindow{}, fmt.Errorf("%w: requested diff window %d > %d bytes", ErrEditRequestTooLarge, maxBytes, maxEditWindowBytes)
	}
	var size int64
	var editedRange func(a, b int64) ([]byte, error)
	if f.Edit != nil && f.Edit.HasEdits() {
		sess := f.Edit
		size = sess.Size()
		editedRange = func(a, b int64) ([]byte, error) {
			return sess.ReadRangeContext(context.Background(), a, b, int64(maxBytes))
		}
	} else {
		size = f.Doc.Size()
		editedRange = func(a, b int64) ([]byte, error) { return f.Doc.ReadRange(a, b) }
	}
	if startByte > size {
		startByte = size
	}
	aligned, err := alignToLineStart(editedRange, startByte, int64(maxBytes))
	if err != nil {
		return DiffWindow{}, err
	}
	aligned, err = alignUTF8WindowStart(editedRange, aligned, size)
	if err != nil {
		return DiffWindow{}, err
	}
	end := boundedEditWindowEnd(aligned, size, int64(maxBytes))
	ed, err := editedRange(aligned, end)
	if err != nil {
		return DiffWindow{}, err
	}
	if end < size {
		if nl := lastIndexByte(ed, '\n'); nl >= 0 {
			ed = ed[:nl+1]
			end = aligned + int64(nl+1)
		}
	}
	ed, trimmed, err := trimEditableUTF8Suffix(ed, end >= size, "diff window")
	if err != nil {
		return DiffWindow{}, err
	}
	end -= int64(trimmed)
	docSize := f.Doc.Size()
	oStart, oEnd := aligned, end
	if f.Edit != nil && f.Edit.HasEdits() {
		mapped, ok := f.Edit.TransformedRangeToSource(aligned, end)
		if !ok {
			return DiffWindow{}, fmt.Errorf("%w: edited range [%d,%d)", ErrDiffCoordinateMapping, aligned, end)
		}
		oStart, oEnd = mapped.Start, mapped.End
	}
	if oStart < 0 || oEnd < oStart || oEnd > docSize {
		return DiffWindow{}, fmt.Errorf(
			"%w: edited range [%d,%d) mapped outside source size %d as [%d,%d)",
			ErrDiffCoordinateMapping, aligned, end, docSize, oStart, oEnd,
		)
	}
	if oEnd-oStart > int64(maxBytes) {
		return DiffWindow{}, fmt.Errorf("%w: mapped original diff span %d > %d bytes", ErrEditRequestTooLarge, oEnd-oStart, maxBytes)
	}
	var orig []byte
	if oEnd > oStart {
		if f.Edit != nil && f.Edit.HasEdits() {
			orig, err = f.Edit.ReadSourceRangeContext(context.Background(), oStart, oEnd, int64(maxBytes))
		} else {
			orig, err = f.Doc.ReadRange(oStart, oEnd)
		}
		if err != nil {
			return DiffWindow{}, err
		}
		if err := validateEditableWindowBytes(orig, "original diff window"); err != nil {
			return DiffWindow{}, err
		}
	}
	return DiffWindow{
		StartByte:         aligned,
		NextByte:          end,
		EditedStartByte:   aligned,
		EditedNextByte:    end,
		OriginalStartByte: oStart,
		OriginalNextByte:  oEnd,
		Original:          string(orig),
		Edited:            string(ed),
		AtBOF:             aligned == 0,
		AtEOF:             end >= size,
	}, nil
}

// boundedEditWindowEnd returns min(start+budget, size) without evaluating an
// overflowing addition. Callers validate budget and clamp start into [0,size].
func boundedEditWindowEnd(start, size, budget int64) int64 {
	if budget <= 0 || start >= size {
		return start
	}
	if budget >= size-start {
		return size
	}
	return start + budget
}

func validateEditableWindowBytes(raw []byte, label string) error {
	if bytes.IndexByte(raw, '\r') >= 0 {
		return fmt.Errorf("%w: %s contains CR or CRLF line endings", ErrFileNotEditable, label)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return fmt.Errorf("%w: %s contains NUL bytes", ErrFileNotEditable, label)
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("%w: %s is not valid UTF-8 text", ErrFileNotEditable, label)
	}
	return nil
}

func alignUTF8WindowStart(readRange func(a, b int64) ([]byte, error), start, size int64) (int64, error) {
	if start <= 0 || start >= size {
		return start, nil
	}
	probeStart := start - (utf8.UTFMax - 1)
	if probeStart < 0 {
		probeStart = 0
	}
	probeEnd := boundedEditWindowEnd(start, size, utf8.UTFMax)
	probe, err := readRange(probeStart, probeEnd)
	if err != nil {
		return 0, err
	}
	relative := int(start - probeStart)
	if relative >= len(probe) || probe[relative]&0xc0 != 0x80 {
		return start, nil
	}
	for candidate := relative - 1; candidate >= 0 && relative-candidate < utf8.UTFMax; candidate-- {
		if probe[candidate]&0xc0 == 0x80 {
			continue
		}
		_, width := utf8.DecodeRune(probe[candidate:])
		if width > 1 && candidate+width > relative && candidate+width <= len(probe) && utf8.Valid(probe[candidate:candidate+width]) {
			return probeStart + int64(candidate+width), nil
		}
		break
	}
	return 0, fmt.Errorf("%w: byte offset %d begins at an isolated UTF-8 continuation byte", ErrFileNotEditable, start)
}

func trimEditableUTF8Suffix(raw []byte, atEOF bool, label string) ([]byte, int, error) {
	if bytes.IndexByte(raw, '\r') >= 0 {
		return nil, 0, fmt.Errorf("%w: %s contains CR or CRLF line endings", ErrFileNotEditable, label)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil, 0, fmt.Errorf("%w: %s contains NUL bytes", ErrFileNotEditable, label)
	}
	if utf8.Valid(raw) {
		return raw, 0, nil
	}
	if !atEOF {
		for trim := 1; trim < utf8.UTFMax && trim <= len(raw); trim++ {
			prefix := raw[:len(raw)-trim]
			suffix := raw[len(raw)-trim:]
			if utf8.Valid(prefix) && !utf8.FullRune(suffix) {
				return prefix, trim, nil
			}
		}
	}
	return nil, 0, fmt.Errorf("%w: %s is not valid UTF-8 text", ErrFileNotEditable, label)
}

// StageEdit reconciles the window [startByte, startByte+origLen) with newText.
// It trims the common prefix/suffix so only the genuinely changed bytes are
// staged, keeping the diff granular and in-place patches minimal.
func (s *FileService) StageEdit(fileID string, startByte int64, origLen int64, newText string) (StagingState, error) {
	if err := validateStageEditRequest(startByte, origLen, newText); err != nil {
		return StagingState{}, err
	}
	s.editWorkMu.Lock()
	defer s.editWorkMu.Unlock()
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return StagingState{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if err := requireEditableFile(f); err != nil {
		return StagingState{}, err
	}
	endByte, ok := checkedRangeEnd(startByte, origLen)
	if !ok {
		return StagingState{}, errors.New("edit range is invalid")
	}
	sess := f.Edit
	if sess == nil || !s.hasPreparedEdit(fileID) {
		return StagingState{}, ErrEditPreparationRequired
	}
	if !sess.MatchesSourceGeneration(f.Path, f.Generation) {
		return StagingState{}, manualedit.ErrSessionGenerationChanged
	}
	if endByte > sess.Size() {
		return StagingState{}, errors.New("edit range is outside the staged document")
	}
	oldBytes, err := sess.ReadRangeContext(context.Background(), startByte, endByte, int64(maxEditWindowBytes))
	if err != nil {
		return StagingState{}, err
	}
	if bytes.IndexByte(oldBytes, '\r') >= 0 {
		return StagingState{}, fmt.Errorf("%w: edited range contains CR or CRLF", ErrFileNotEditable)
	}
	if bytes.IndexByte(oldBytes, 0) >= 0 || !utf8.Valid(oldBytes) {
		return StagingState{}, fmt.Errorf("%w: edited range is not valid UTF-8 text", ErrFileNotEditable)
	}
	newBytes := []byte(newText)

	// Trim the common prefix and suffix so only genuinely changed bytes become
	// part of the bounded undo history.
	pre := 0
	for pre < len(oldBytes) && pre < len(newBytes) && oldBytes[pre] == newBytes[pre] {
		pre++
	}
	suf := 0
	for suf < len(oldBytes)-pre && suf < len(newBytes)-pre &&
		oldBytes[len(oldBytes)-1-suf] == newBytes[len(newBytes)-1-suf] {
		suf++
	}

	editStart := startByte + int64(pre)
	editEnd := startByte + int64(len(oldBytes)-suf)
	editText := newBytes[pre : len(newBytes)-suf]
	if editEnd == editStart && len(editText) == 0 {
		return stagingState(f), nil
	}
	if err := sess.ApplyEdit(manualedit.Edit{Start: editStart, End: editEnd, Text: editText}); err != nil {
		return StagingState{}, err
	}
	return stagingState(f), nil
}

// DiscardEdits drops all staged edits.
func (s *FileService) DiscardEdits(fileID string) (StagingState, error) {
	// manualedit.Session is intentionally single-owner. Serialize discard with
	// StageEdit so a renderer close cannot reset the piece table while an
	// already-dispatched debounced stage is still mutating it.
	s.editWorkMu.Lock()
	defer s.editWorkMu.Unlock()
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return StagingState{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if f.Edit != nil {
		if err := f.Edit.DiscardEdits(); err != nil {
			return StagingState{}, err
		}
	}
	f.ResetEdits()
	s.releasePreparedEdit(fileID)
	return stagingState(f), nil
}

// GetStagingState reports current pending-edit status.
func (s *FileService) GetStagingState(fileID string) (StagingState, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return StagingState{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	return stagingState(f), nil
}

// saveCopy writes the edited file to dstPath via the streaming copy-through
// pipeline (source untouched; staged edits remain).
func (s *FileService) saveCopy(fileID string, dstPath string) (SaveResult, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return SaveResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	approval, err := captureSaveCopyApproval(f)
	f.Release()
	if err != nil {
		return SaveResult{}, err
	}
	return s.saveCopyApproved(fileID, dstPath, approval)
}

func (s *FileService) saveCopyApproved(fileID string, dstPath string, approval saveCopyApproval) (SaveResult, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return SaveResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if !approval.matches(f) {
		return SaveResult{}, ErrSaveCopyStateChanged
	}
	if !f.Edit.MatchesSourceGeneration(f.Path, f.Generation) {
		return SaveResult{}, manualedit.ErrSessionGenerationChanged
	}
	bytesWritten, err := withFileJobResult(s, fileID, "Save edited copy", jobKindTransform, func(ctx context.Context, progress func(int64, string)) (int64, error) {
		progress(0, "verifying source")
		if err := f.Edit.ValidateSourceContext(ctx, func(completed, _ int64) {
			progress(completed, "verifying prepared source")
		}); err != nil {
			return 0, err
		}
		return writeSafeOutputValidatedContext(ctx, f.Doc, f.Path, dstPath, func(dst io.Writer) error {
			_, err := f.Edit.WriteBoundTo(ctx, dst, func(p manualedit.Progress) {
				progress(p.BytesWritten, "bytes written")
			})
			return err
		}, func(validateCtx context.Context) error {
			progress(f.Edit.Size(), "revalidating source")
			return f.Edit.ValidateSourceContext(validateCtx, nil)
		})
	})
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Mode: "copy", BytesWritten: bytesWritten, OutputPath: dstPath}, nil
}

func validateStageEditRequest(startByte int64, origLen int64, newText string) error {
	if startByte < 0 || origLen < 0 || origLen > int64(maxEditWindowBytes) || startByte > math.MaxInt64-origLen {
		return fmt.Errorf("%w: invalid range start=%d length=%d", ErrEditRequestTooLarge, startByte, origLen)
	}
	if len(newText) > maxEditWindowBytes {
		return fmt.Errorf("%w: edited text %d > %d bytes", ErrEditRequestTooLarge, len(newText), maxEditWindowBytes)
	}
	if strings.ContainsRune(newText, '\r') {
		return fmt.Errorf("%w: edited text contains CR or CRLF", ErrFileNotEditable)
	}
	if strings.ContainsRune(newText, 0) || !utf8.ValidString(newText) {
		return fmt.Errorf("%w: edited text is not valid UTF-8 text", ErrFileNotEditable)
	}
	return nil
}

func requireEditableFile(file *session.File) error {
	if file == nil || file.Doc == nil {
		return ErrFileNotEditable
	}
	metadata := file.Doc.Metadata()
	if metadata.Binary || !editableEncoding(metadata.Encoding, metadata.LineEnding) {
		return ErrFileNotEditable
	}
	return nil
}

// SavePatch is retained as an RPC compatibility boundary but fails closed.
// Source mutation remains unavailable; callers must save a separate copy.
func (s *FileService) SavePatch(fileID string) (SaveResult, error) {
	_ = s
	_ = fileID
	return SaveResult{}, ErrInPlaceSaveDisabled
}

func stagingState(f *session.File) StagingState {
	if f.Edit == nil || !f.Edit.HasEdits() {
		size := f.Doc.Size()
		return StagingState{OriginalSize: size, EditedSize: size}
	}
	orig := f.Edit.OriginalSize()
	edited := f.Edit.Size()
	net := edited - orig
	lengthPreserving := net == 0
	return StagingState{
		EditCount:        f.Edit.EditCount(),
		OriginalSize:     orig,
		EditedSize:       edited,
		NetDelta:         net,
		LengthPreserving: lengthPreserving,
		InPlaceEligible:  false,
	}
}

func lastIndexByte(b []byte, c byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// alignToLineStart returns the offset of the line start at or before start,
// scanning back at most maxScan bytes.
func alignToLineStart(readRange func(a, b int64) ([]byte, error), start int64, maxScan int64) (int64, error) {
	if start <= 0 {
		return 0, nil
	}
	from := start - maxScan
	if from < 0 {
		from = 0
	}
	buf, err := readRange(from, start)
	if err != nil {
		return 0, err
	}
	if nl := lastIndexByte(buf, '\n'); nl >= 0 {
		return from + int64(nl) + 1, nil
	}
	// No line boundary exists within the bounded backward scan. Starting at the
	// requested offset keeps the window bounded and guarantees forward progress;
	// callers treat it as a long-line fragment rather than an exact line start.
	return start, nil
}
