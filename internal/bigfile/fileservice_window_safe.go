package bigfile

import (
	"errors"
	"fmt"
	"strings"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/session"
)

const (
	// Browser bridge numbers are IEEE-754 doubles. Refuse file coordinates that
	// cannot make a lossless round trip instead of silently navigating to a
	// neighboring byte.
	maxJavaScriptSafeInteger int64 = 1<<53 - 1

	windowExactLineScanBytes int64 = 1 << 20
	maxWindowDecodedBytes          = 8 << 20
)

// FileStateResult is a cheap follow-tail poll. Milliseconds (rather than Unix
// nanoseconds) keep the timestamp exactly representable by JavaScript.
type FileStateResult struct {
	Size              int64 `json:"size"`
	ModTimeUnixMillis int64 `json:"modTimeUnixMillis"`
	SameOpenedFile    bool  `json:"sameOpenedFile"`
	ChangedFromOpen   bool  `json:"changedFromOpen"`
}

// FileState returns the pathname's current state and whether it still names
// the exact retained descriptor. This detects rename-and-recreate rotation,
// including replacements with the same size and modification time.
func (s *FileService) FileState(fileID string) (FileStateResult, error) {
	if err := validateSearchFileID(fileID); err != nil {
		return FileStateResult{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return FileStateResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()

	state, same, err := f.Doc.CurrentPathIdentity()
	if err != nil {
		return FileStateResult{}, err
	}
	modMillis := state.ModTime.UnixMilli()
	if err := validateJavaScriptSafeInteger("file size", state.Size); err != nil {
		return FileStateResult{}, err
	}
	if err := validateJavaScriptSafeInteger("file modification time", modMillis); err != nil {
		return FileStateResult{}, err
	}
	original := f.Doc.OriginalFileState()
	changed := !same || !state.Equal(original)
	return FileStateResult{
		Size:              state.Size,
		ModTimeUnixMillis: modMillis,
		SameOpenedFile:    same,
		ChangedFromOpen:   changed,
	}, nil
}

// MatchWindow resolves a raw search match to exact CodeMirror UTF-16 code-unit
// coordinates in one bounded decoded window.
type MatchWindow struct {
	Window Window `json:"window"`
	Found  bool   `json:"found"`
	From   int    `json:"from"`
	To     int    `json:"to"`
}

// GetMatchWindow returns a bounded decoded window that contains hitOffset and,
// when the complete hit is displayable, its exact JavaScript UTF-16 span.
func (s *FileService) GetMatchWindow(fileID string, hitOffset int64, hitLength int, maxBytes int) (MatchWindow, error) {
	if err := validateSearchFileID(fileID); err != nil {
		return MatchWindow{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return MatchWindow{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	budget, err := validateStrictWindowBudget(maxBytes)
	if err != nil {
		return MatchWindow{}, err
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return MatchWindow{}, err
	}
	size := f.Doc.Size()
	if err := validateJavaScriptSafeInteger("file size", size); err != nil {
		return MatchWindow{}, err
	}
	if hitOffset < 0 || hitOffset > size || hitLength < 0 || int64(hitLength) > size-hitOffset {
		return MatchWindow{}, errors.New("search hit is outside the opened source")
	}
	if hitLength > budget {
		return MatchWindow{}, errors.New("search hit is too large for the bounded match window")
	}

	margin := min(256, (budget-hitLength)/2)
	hint := hitOffset - int64(margin)
	if hint < 0 {
		hint = 0
	}
	matchEnd := hitOffset + int64(hitLength)
	if matchEnd-hint > int64(budget) {
		hint = matchEnd - int64(budget)
	}
	rendered, err := renderBoundedWindow(f, hint, budget, -1)
	if err != nil {
		return MatchWindow{}, err
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return MatchWindow{}, err
	}
	for _, row := range rendered.rows {
		from, fromOK := row.utf16Position(hitOffset)
		to, toOK := row.utf16Position(matchEnd)
		if fromOK && toOK && to >= from {
			return MatchWindow{Window: rendered.window, Found: true, From: from, To: to}, nil
		}
	}
	return MatchWindow{Window: rendered.window}, nil
}

// GetTailWindow returns the final non-empty bounded window ending at the
// retained generation's EOF. An empty source honestly returns an empty
// BOF/EOF window.
func (s *FileService) GetTailWindow(fileID string, maxBytes int) (Window, error) {
	if err := validateSearchFileID(fileID); err != nil {
		return Window{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return Window{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	budget, err := validateStrictWindowBudget(maxBytes)
	if err != nil {
		return Window{}, err
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return Window{}, err
	}
	size := f.Doc.Size()
	if err := validateJavaScriptSafeInteger("file size", size); err != nil {
		return Window{}, err
	}
	if size == 0 {
		return emptyBoundedWindow(f.ID, 0), nil
	}
	start, err := f.Doc.PreviousWindowStart(size, int64(budget), windowLineTarget)
	if err != nil {
		return Window{}, err
	}
	rendered, err := renderBoundedWindow(f, start, budget, size)
	if err != nil {
		return Window{}, err
	}
	if rendered.window.NextByte != size || rendered.window.NextByte <= rendered.window.StartByte {
		return Window{}, errors.New("tail window did not consume the final non-empty source range")
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return Window{}, err
	}
	return rendered.window, nil
}

func validateStrictWindowBudget(maxBytes int) (int, error) {
	if maxBytes < 0 {
		return 0, errors.New("window byte budget must not be negative")
	}
	if maxBytes == 0 {
		return defaultWindowBytes, nil
	}
	if maxBytes > defaultWindowBytes {
		return 0, fmt.Errorf("window byte budget %d exceeds hard limit %d", maxBytes, defaultWindowBytes)
	}
	return maxBytes, nil
}

func validateJavaScriptSafeInteger(name string, value int64) error {
	if value < -maxJavaScriptSafeInteger || value > maxJavaScriptSafeInteger {
		return fmt.Errorf("%s %d is outside JavaScript's exact integer range", name, value)
	}
	return nil
}

type boundedRenderedWindow struct {
	window Window
	rows   []boundedRenderedRow
}

type boundedRenderedRow struct {
	rawStart       int64
	rawEnd         int64
	textStartUTF16 int
	text           string
	rawBoundaries  []int
}

func (r boundedRenderedRow) utf16Position(rawOffset int64) (int, bool) {
	if rawOffset < r.rawStart || rawOffset > r.rawEnd || len(r.rawBoundaries) == 0 {
		return 0, false
	}
	relative := int(rawOffset - r.rawStart)
	// A leading BOM occupies source bytes but no rendered code units.
	if relative <= r.rawBoundaries[0] {
		return r.textStartUTF16, true
	}
	units := 0
	runeIndex := 0
	for _, decoded := range r.text {
		if decoded > 0xFFFF {
			units += 2
		} else {
			units++
		}
		runeIndex++
		if runeIndex < len(r.rawBoundaries) && r.rawBoundaries[runeIndex] == relative {
			return r.textStartUTF16 + units, true
		}
	}
	return 0, false
}

func renderBoundedWindow(f *session.File, requestedStart int64, budget int, exclusiveEnd int64) (boundedRenderedWindow, error) {
	return renderBoundedWindowAligned(f, requestedStart, budget, false, exclusiveEnd)
}

func renderBoundedWindowAligned(f *session.File, requestedStart int64, budget int, alignLine bool, exclusiveEnd int64) (boundedRenderedWindow, error) {
	if f == nil || f.Doc == nil {
		return boundedRenderedWindow{}, errors.New("opened file is required")
	}
	budget, err := validateStrictWindowBudget(budget)
	if err != nil {
		return boundedRenderedWindow{}, err
	}
	if requestedStart < 0 {
		return boundedRenderedWindow{}, errors.New("window start must not be negative")
	}
	if err := validateJavaScriptSafeInteger("window start", requestedStart); err != nil {
		return boundedRenderedWindow{}, err
	}
	requestedStart = f.Doc.ClampOffset(requestedStart)
	meta := f.Doc.Metadata()
	if meta.EncodingRequiresConfirmation {
		return boundedRenderedWindow{}, fmt.Errorf("%w: %s", encodingx.ErrEncodingConfirmationRequired, meta.Encoding)
	}

	start := requestedStart
	if alignLine {
		start, _, err = f.Doc.AlignedWindowStart(requestedStart, int64(min(budget, rowDisplayBytes)))
	} else {
		start, err = f.Doc.AlignTextOffsetBackward(requestedStart)
	}
	if err != nil {
		return boundedRenderedWindow{}, err
	}
	size := f.Doc.Size()
	end := boundedReadEnd(start, size, budget)
	if exclusiveEnd >= 0 {
		if exclusiveEnd < start || exclusiveEnd > size || exclusiveEnd-start > int64(budget) {
			return boundedRenderedWindow{}, errors.New("exclusive window end is outside the bounded source range")
		}
		end = exclusiveEnd
	}
	alignedEnd, err := f.Doc.AlignTextOffsetBackward(end)
	if err != nil {
		return boundedRenderedWindow{}, err
	}
	if exclusiveEnd >= 0 && alignedEnd != end {
		return boundedRenderedWindow{}, fmt.Errorf("%w: exclusive window end %d", document.ErrTextBoundary, end)
	}
	end = alignedEnd
	if end == start && start < size {
		return boundedRenderedWindow{}, errors.New("window byte budget is too small for one complete source character")
	}
	firstLine, approximate := boundedWindowFirstLine(f.Doc, start)
	if start == size {
		return boundedRenderedWindow{window: emptyBoundedWindow(f.ID, start)}, nil
	}

	rawBudget := int(end - start)
	if _, err := f.Doc.DecodeAlignedRange(start, end, int64(rawBudget)); err != nil {
		return boundedRenderedWindow{}, err
	}
	maxLineBytes := min(rowDisplayBytes, rawBudget)
	visualLineLimit := windowLineTarget + (rawBudget+maxLineBytes-1)/maxLineBytes
	page, err := f.Doc.VisiblePageFromOffset(start, visualLineLimit, document.VisibleLineOptions{
		MaxBytes:        rawBudget,
		MaxLineBytes:    maxLineBytes,
		FirstLineNumber: firstLine,
	})
	if err != nil {
		return boundedRenderedWindow{}, err
	}
	if page.NextOffset > end {
		return boundedRenderedWindow{}, errors.New("decoded window exceeded its validated raw-byte budget")
	}

	result := boundedRenderedWindow{
		window: Window{
			FileID:      f.ID,
			StartByte:   page.StartOffset,
			NextByte:    page.NextOffset,
			LineOffsets: []int64{},
			LineNumbers: []int64{},
			AtBOF:       page.StartOffset == 0,
			AtEOF:       page.NextOffset >= size,
			Approx:      approximate,
		},
		rows: make([]boundedRenderedRow, 0, len(page.Lines)),
	}
	var text strings.Builder
	textUnits := 0
	lastLineNumber := int64(-1)
	logicalLines := 0
	for _, visual := range page.Lines {
		if logicalLines > 0 && visual.LineNumber == lastLineNumber {
			continue
		}
		if logicalLines == windowLineTarget {
			result.window.NextByte = visual.DisplayOffset
			result.window.AtEOF = result.window.NextByte >= size
			break
		}
		logicalLines++
		visual = truncateBoundedVisualLine(visual, maxRowDisplayRunes)
		separatorBytes := 0
		if len(result.rows) > 0 {
			separatorBytes = 1
		}
		marker := ""
		if visual.Truncated || visual.HasRightHidden {
			marker = " \u22ef"
		}
		if text.Len()+separatorBytes+len(visual.Text)+len(marker) > maxWindowDecodedBytes {
			return boundedRenderedWindow{}, errors.New("decoded window exceeds the hard text allocation limit")
		}
		if separatorBytes != 0 {
			text.WriteByte('\n')
			textUnits++
		}
		textStart := textUnits
		text.WriteString(visual.Text)
		text.WriteString(marker)
		textUnits += utf16CodeUnits(visual.Text) + utf16CodeUnits(marker)
		result.rows = append(result.rows, boundedRenderedRow{
			rawStart:       visual.DisplayOffset,
			rawEnd:         visual.DisplayEndOffset,
			textStartUTF16: textStart,
			text:           visual.Text,
			rawBoundaries:  visual.DisplayRuneByteOffsets,
		})
		result.window.LineOffsets = append(result.window.LineOffsets, visual.DisplayOffset)
		result.window.LineNumbers = append(result.window.LineNumbers, visual.LineNumber)
		lastLineNumber = visual.LineNumber
	}
	result.window.Text = text.String()
	return result, nil
}

func boundedWindowForFile(f *session.File, requestedStart int64, maxBytes int, alignLine bool, exclusiveEnd int64) (Window, error) {
	if f == nil || f.Doc == nil {
		return Window{}, errors.New("opened file is required")
	}
	budget, err := validateStrictWindowBudget(maxBytes)
	if err != nil {
		return Window{}, err
	}
	if requestedStart < 0 {
		return Window{}, errors.New("window start must not be negative")
	}
	if err := validateJavaScriptSafeInteger("window start", requestedStart); err != nil {
		return Window{}, err
	}
	if exclusiveEnd >= 0 {
		if err := validateJavaScriptSafeInteger("exclusive window end", exclusiveEnd); err != nil {
			return Window{}, err
		}
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return Window{}, err
	}
	if err := validateJavaScriptSafeInteger("file size", f.Doc.Size()); err != nil {
		return Window{}, err
	}
	rendered, err := renderBoundedWindowAligned(f, requestedStart, budget, alignLine, exclusiveEnd)
	if err != nil {
		return Window{}, err
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return Window{}, err
	}
	return rendered.window, nil
}

func previousBoundedWindowForFile(f *session.File, currentStart int64, maxBytes int) (Window, error) {
	if f == nil || f.Doc == nil {
		return Window{}, errors.New("opened file is required")
	}
	budget, err := validateStrictWindowBudget(maxBytes)
	if err != nil {
		return Window{}, err
	}
	if currentStart < 0 {
		return Window{}, errors.New("previous-window start must not be negative")
	}
	if err := validateJavaScriptSafeInteger("previous-window start", currentStart); err != nil {
		return Window{}, err
	}
	currentStart = f.Doc.ClampOffset(currentStart)
	if currentStart <= 0 {
		return boundedWindowForFile(f, 0, budget, true, -1)
	}
	boundary, err := f.Doc.AlignTextOffsetBackward(currentStart)
	if err != nil {
		return Window{}, err
	}
	if boundary != currentStart {
		return Window{}, fmt.Errorf("%w: previous-window end %d", document.ErrTextBoundary, currentStart)
	}
	start, err := f.Doc.PreviousWindowStart(currentStart, int64(budget), windowLineTarget)
	if err != nil {
		return Window{}, err
	}
	window, err := boundedWindowForFile(f, start, budget, false, currentStart)
	if err != nil {
		return Window{}, err
	}
	if window.NextByte != currentStart {
		return Window{}, fmt.Errorf("previous window is not adjacent: next byte %d, expected %d", window.NextByte, currentStart)
	}
	return window, nil
}

func emptyBoundedWindow(fileID string, offset int64) Window {
	return Window{
		FileID:      fileID,
		StartByte:   offset,
		NextByte:    offset,
		Text:        "",
		LineOffsets: []int64{},
		LineNumbers: []int64{},
		AtBOF:       offset == 0,
		AtEOF:       true,
	}
}

func boundedWindowFirstLine(doc *document.FileDocument, start int64) (int64, bool) {
	if start == 0 {
		return 1, false
	}
	if exact, ok, err := doc.ExactOffsetToLineWithin(start, windowExactLineScanBytes); err == nil && ok && exact > 0 {
		return exact, false
	}
	if approximate, ok := doc.ApproxOffsetToLine(start); ok && approximate > 0 {
		return approximate, true
	}
	return 1, true
}

func truncateBoundedVisualLine(line document.VisualLine, maxRunes int) document.VisualLine {
	if maxRunes <= 0 || len(line.DisplayRuneByteOffsets) <= maxRunes+1 {
		return line
	}
	byteEnd := 0
	count := 0
	for index := range line.Text {
		if count == maxRunes {
			byteEnd = index
			break
		}
		count++
	}
	if byteEnd == 0 && count < maxRunes {
		return line
	}
	line.Text = line.Text[:byteEnd]
	line.DisplayRuneByteOffsets = line.DisplayRuneByteOffsets[:maxRunes+1]
	line.DisplayEndOffset = line.DisplayOffset + int64(line.DisplayRuneByteOffsets[maxRunes])
	line.Truncated = true
	line.HasRightHidden = true
	return line
}

func utf16CodeUnits(text string) int {
	units := 0
	for _, decoded := range text {
		if decoded > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}
