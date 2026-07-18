package bigfile

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/bigfile/inplace"
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
		oldBytes, err := f.Doc.ReadRange(e.Start, oldEnd)
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

// SaveCopyViaDialog shows a native save dialog and writes the edited copy there.
// A cancelled dialog returns an empty SaveResult (Mode == "") with nil error.
func (s *FileService) SaveCopyViaDialog(fileID string) (SaveResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return SaveResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	f.Release()
	dst, err := application.Get().Dialog.SaveFile().
		SetMessage("Save edited copy as").
		PromptForSingleSelection()
	if err != nil {
		return SaveResult{}, err
	}
	if strings.TrimSpace(dst) == "" {
		return SaveResult{}, nil // cancelled
	}
	return s.SaveCopy(fileID, dst)
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

func sidecarPath(path string) string { return path + ".qrp" }

// editableEncoding reports whether a file can be edited in v1: byte-exact
// round-tripping is only guaranteed for UTF-8/ASCII with LF line endings.
func editableEncoding(encoding, lineEnding string) bool {
	enc := strings.ToLower(strings.TrimSpace(encoding))
	utf8ish := strings.Contains(enc, "utf-8") || strings.Contains(enc, "utf8") || strings.Contains(enc, "ascii") || enc == ""
	le := strings.ToLower(lineEnding)
	crlf := strings.Contains(le, "crlf") || strings.Contains(lineEnding, "\r")
	return utf8ish && !crlf
}

// GetEditWindow returns a byte-exact, line-aligned UTF-8 window that reflects
// staged edits (reads the edited view when edits exist, else the raw document).
// startByte is aligned down to a line start; pass any byte for go-to / prev.
func (s *FileService) GetEditWindow(fileID string, startByte int64, maxBytes int) (Window, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return Window{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	maxBytes = clampRequestInt(maxBytes, editWindowBytes, maxEditWindowBytes)

	var size int64
	var readRange func(a, b int64) ([]byte, error)
	if f.Edit != nil && f.Edit.HasEdits() {
		sess := f.Edit
		size = sess.Size()
		readRange = func(a, b int64) ([]byte, error) { return sess.ReadRange(f.Doc, a, b) }
	} else {
		size = f.Doc.Size()
		readRange = func(a, b int64) ([]byte, error) { return f.Doc.ReadRange(a, b) }
	}

	if startByte < 0 {
		startByte = 0
	}
	if startByte > size {
		startByte = size
	}

	aligned, err := alignToLineStart(readRange, startByte, int64(maxBytes))
	if err != nil {
		return Window{}, err
	}
	readEnd := boundedReadEnd(aligned, size, maxBytes)
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

// DiffWindow holds the original vs edited text for a window, for side-by-side
// review. (Offsets align exactly for length-preserving edits; for
// length-changing edits the diff is still computed but may show a tail.)
type DiffWindow struct {
	StartByte int64  `json:"startByte"`
	NextByte  int64  `json:"nextByte"`
	Original  string `json:"original"`
	Edited    string `json:"edited"`
	AtBOF     bool   `json:"atBof"`
	AtEOF     bool   `json:"atEof"`
}

// GetDiffWindow returns the edited and original text for a line-aligned window,
// for the side-by-side diff view.
func (s *FileService) GetDiffWindow(fileID string, startByte int64, maxBytes int) (DiffWindow, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return DiffWindow{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	maxBytes = clampRequestInt(maxBytes, editWindowBytes, maxEditWindowBytes)
	var size int64
	var editedRange func(a, b int64) ([]byte, error)
	if f.Edit != nil && f.Edit.HasEdits() {
		sess := f.Edit
		size = sess.Size()
		editedRange = func(a, b int64) ([]byte, error) { return sess.ReadRange(f.Doc, a, b) }
	} else {
		size = f.Doc.Size()
		editedRange = func(a, b int64) ([]byte, error) { return f.Doc.ReadRange(a, b) }
	}
	if startByte < 0 {
		startByte = 0
	}
	if startByte > size {
		startByte = size
	}
	aligned, err := alignToLineStart(editedRange, startByte, int64(maxBytes))
	if err != nil {
		return DiffWindow{}, err
	}
	end := boundedReadEnd(aligned, size, maxBytes)
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
	docSize := f.Doc.Size()
	oStart, oEnd := aligned, end
	if oStart > docSize {
		oStart = docSize
	}
	if oEnd > docSize {
		oEnd = docSize
	}
	var orig []byte
	if oEnd > oStart {
		if orig, err = f.Doc.ReadRange(oStart, oEnd); err != nil {
			return DiffWindow{}, err
		}
	}
	return DiffWindow{
		StartByte: aligned,
		NextByte:  end,
		Original:  string(orig),
		Edited:    string(ed),
		AtBOF:     aligned == 0,
		AtEOF:     end >= size,
	}, nil
}

// StageEdit reconciles the window [startByte, startByte+origLen) with newText.
// It trims the common prefix/suffix so only the genuinely changed bytes are
// staged — keeping the diff granular and in-place patches minimal.
func (s *FileService) StageEdit(fileID string, startByte int64, origLen int64, newText string) (StagingState, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return StagingState{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if origLen > int64(maxEditWindowBytes) || len(newText) > maxEditWindowBytes {
		return StagingState{}, errors.New("edit exceeds the bounded window limit")
	}
	endByte, ok := checkedRangeEnd(startByte, origLen)
	if !ok {
		return StagingState{}, errors.New("edit range is invalid")
	}
	sess := f.EditSession()
	if endByte > sess.Size() {
		return StagingState{}, errors.New("edit range is outside the staged document")
	}
	oldBytes, err := sess.ReadRange(f.Doc, startByte, endByte)
	if err != nil {
		return StagingState{}, err
	}
	newBytes := []byte(newText)

	// Common prefix.
	pre := 0
	for pre < len(oldBytes) && pre < len(newBytes) && oldBytes[pre] == newBytes[pre] {
		pre++
	}
	// Common suffix (not overlapping the prefix).
	suf := 0
	for suf < len(oldBytes)-pre && suf < len(newBytes)-pre &&
		oldBytes[len(oldBytes)-1-suf] == newBytes[len(newBytes)-1-suf] {
		suf++
	}

	editStart := startByte + int64(pre)
	editEnd := startByte + int64(len(oldBytes)-suf)
	editText := newBytes[pre : len(newBytes)-suf]
	if editEnd == editStart && len(editText) == 0 {
		return stagingState(f), nil // no net change
	}
	if err := sess.ApplyEdit(manualedit.Edit{Start: editStart, End: editEnd, Text: editText}); err != nil {
		return StagingState{}, err
	}
	return stagingState(f), nil
}

// DiscardEdits drops all staged edits.
func (s *FileService) DiscardEdits(fileID string) (StagingState, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return StagingState{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	f.ResetEdits()
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

// SaveCopy writes the edited file to dstPath via the streaming copy-through
// pipeline (source untouched; staged edits remain).
func (s *FileService) SaveCopy(fileID string, dstPath string) (SaveResult, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return SaveResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if f.Edit == nil || !f.Edit.HasEdits() {
		return SaveResult{}, errors.New("no staged edits")
	}
	summary, err := withJobResult(s, "Save edited copy", func(ctx context.Context, progress func(int64, string)) (manualedit.FileSummary, error) {
		return manualedit.WriteSessionToFile(ctx, f.Path, dstPath, f.Edit, manualedit.FileOptions{
			Progress: func(p manualedit.Progress) { progress(p.BytesWritten, "bytes written") },
		})
	})
	if err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Mode: "copy", BytesWritten: summary.BytesWritten, OutputPath: summary.OutputPath}, nil
}

// SavePatch applies the staged edits in place (length-preserving only) with a
// crash-safe reverse-patch sidecar, then refreshes the document from disk.
func (s *FileService) SavePatch(fileID string) (SaveResult, error) {
	f, ok := s.reg.GetEdit(fileID)
	if !ok {
		return SaveResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	released := false
	defer func() {
		if !released {
			f.Release()
		}
	}()
	if f.Edit == nil || !f.Edit.HasEdits() {
		return SaveResult{}, errors.New("no staged edits")
	}
	if !stagingState(f).InPlaceEligible {
		return SaveResult{}, errors.New("edits change the file length; use Save as copy")
	}

	edits := f.Edit.SourceMappedActiveEdits()
	patches := make([]inplace.Patch, 0, len(edits))
	for _, e := range edits {
		if int64(len(e.Text)) != e.End-e.Start {
			return SaveResult{}, errors.New("edits change the file length; use Save as copy")
		}
		old, err := f.Doc.ReadRange(e.Start, e.End)
		if err != nil {
			return SaveResult{}, err
		}
		patches = append(patches, inplace.Patch{Offset: e.Start, Old: old, New: append([]byte(nil), e.Text...)})
	}

	// inplace.Apply commits then deletes the sidecar on success, so it is not a
	// durable "undo" artifact and must not be reported as one. A sidecar left by
	// an interrupted save is preserved for an explicit recovery workflow; merely
	// opening the source must never trust and replay an adjacent file.
	if err := inplace.Apply(f.Path, patches, sidecarPath(f.Path)); err != nil {
		return SaveResult{}, err
	}
	path := f.Path
	f.Release()
	released = true
	reopened, err := s.reg.Reopen(fileID)
	if err != nil {
		return SaveResult{}, err
	}
	reopened.Release()
	s.invalidateSQLSummary(fileID)

	var written int64
	for _, p := range patches {
		written += int64(len(p.New))
	}
	return SaveResult{Mode: "patch", BytesWritten: written, OutputPath: path}, nil
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
	inPlace := lengthPreserving
	if inPlace {
		for _, e := range f.Edit.SourceMappedActiveEdits() {
			if int64(len(e.Text)) != e.End-e.Start {
				inPlace = false
				break
			}
		}
	}
	return StagingState{
		EditCount:        f.Edit.EditCount(),
		OriginalSize:     orig,
		EditedSize:       edited,
		NetDelta:         net,
		LengthPreserving: lengthPreserving,
		InPlaceEligible:  inPlace,
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
	if from == 0 {
		return 0, nil
	}
	return from, nil // pathological long line; accept the scan floor
}
