package lineindex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"sync"
)

// Entry maps a known line number to a byte offset.
type Entry struct {
	Line   int64
	Offset int64
}

// Progress describes the current index scan state.
type Progress struct {
	Lines int64
	Bytes int64
	Done  bool
}

// Snapshot is a serializable representation of an index.
type Snapshot struct {
	EveryLines int64
	Entries    []Entry
	Lines      int64
	Bytes      int64
	Done       bool
}

// Index is a sparse line index.
// Store every Nth line to support approximate and later exact navigation.
type Index struct {
	EveryLines int64

	mu              sync.RWMutex
	entries         []Entry
	priorityEntries []Entry
	lines           int64
	bytes           int64
	done            bool
}

func New(everyLines int64) *Index {
	if everyLines <= 0 {
		everyLines = defaultEveryLines
	}
	return &Index{EveryLines: everyLines}
}

// FromSnapshot restores an index snapshot.
func FromSnapshot(s Snapshot) *Index {
	idx := New(s.EveryLines)
	idx.entries = append(idx.entries, s.Entries...)
	idx.lines = s.Lines
	idx.bytes = s.Bytes
	idx.done = s.Done
	return idx
}

func (idx *Index) Entries() []Entry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	out := make([]Entry, len(idx.entries))
	copy(out, idx.entries)
	return out
}

func (idx *Index) Snapshot() Snapshot {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	entries := make([]Entry, len(idx.entries))
	copy(entries, idx.entries)
	return Snapshot{
		EveryLines: idx.EveryLines,
		Entries:    entries,
		Lines:      idx.lines,
		Bytes:      idx.bytes,
		Done:       idx.done,
	}
}

func (idx *Index) Done() bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.done
}

func (idx *Index) Progress() Progress {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return Progress{Lines: idx.lines, Bytes: idx.bytes, Done: idx.done}
}

// MarkDone records a completed empty or externally scanned index state.
func (idx *Index) MarkDone(lines, bytes int64) {
	idx.setDone(lines, bytes)
}

// Build scans a reader sequentially and records sparse line offsets.
func (idx *Index) Build(ctx context.Context, r io.Reader) error {
	return idx.BuildWithEncoding(ctx, r, "UTF-8")
}

// BuildWithEncoding scans a reader sequentially and records sparse line offsets
// using encoding-aware newline detection.
func (idx *Index) BuildWithEncoding(ctx context.Context, r io.Reader, encodingName string) error {
	const chunkSize = 4 * 1024 * 1024

	buf := make([]byte, chunkSize)
	var offset int64
	var line int64
	carry := make([]byte, 0, lineBreakOverlap(encodingName))

	idx.addExact(Entry{Line: 1, Offset: 0})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := r.Read(buf)
		if n > 0 {
			part := buf[:n]
			carry = scanLineBreaks(encodingName, carry, part, offset, func(nextOffset int64) bool {
				line++
				if line%idx.EveryLines == 0 {
					idx.addExact(Entry{Line: line + 1, Offset: nextOffset})
				}
				return true
			})
			offset += int64(n)
			idx.setProgress(line, offset)
		}

		if errors.Is(err, io.EOF) {
			idx.setDone(line, offset)
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func scanLineBreaks(encodingName string, carry []byte, data []byte, baseOffset int64, emit func(nextOffset int64) bool) []byte {
	overlap := lineBreakOverlap(encodingName)
	if len(data) == 0 {
		if overlap > len(carry) {
			overlap = len(carry)
		}
		return append(carry[:0], carry[len(carry)-overlap:]...)
	}

	if overlap > 0 && len(carry) > 0 {
		seam := [2]byte{carry[len(carry)-1], data[0]}
		if idx, width := findLineBreak(seam[:], encodingName); idx == 0 {
			if !emit(baseOffset - int64(len(carry)) + int64(width)) {
				return retainLineBreakCarry(carry, data, overlap)
			}
		}
	}

	searchFrom := 0
	for {
		idx, width := findLineBreak(data[searchFrom:], encodingName)
		if idx < 0 {
			break
		}
		rel := searchFrom + idx
		if !emit(baseOffset + int64(rel+width)) {
			break
		}
		searchFrom = rel + width
	}
	return retainLineBreakCarry(carry, data, overlap)
}

func retainLineBreakCarry(carry []byte, data []byte, overlap int) []byte {
	if overlap <= 0 {
		return carry[:0]
	}
	if overlap > len(data) {
		overlap = len(data)
	}
	return append(carry[:0], data[len(data)-overlap:]...)
}

func lineBreakOverlap(encodingName string) int {
	switch encodingName {
	case "UTF-16LE", "UTF-16BE":
		return 1
	default:
		return 0
	}
}

func findLineBreak(data []byte, encodingName string) (index int, width int) {
	switch encodingName {
	case "UTF-16LE":
		lf := bytes.Index(data, []byte{0x0A, 0x00})
		cr := bytes.Index(data, []byte{0x0D, 0x00})
		if lf >= 0 && (cr < 0 || lf <= cr+2) {
			return lf, 2
		}
		if cr >= 0 {
			return cr, 2
		}
	case "UTF-16BE":
		lf := bytes.Index(data, []byte{0x00, 0x0A})
		cr := bytes.Index(data, []byte{0x00, 0x0D})
		if lf >= 0 && (cr < 0 || lf <= cr+2) {
			return lf, 2
		}
		if cr >= 0 {
			return cr, 2
		}
	default:
		lf := bytes.IndexByte(data, '\n')
		cr := bytes.IndexByte(data, '\r')
		if lf >= 0 && (cr < 0 || lf <= cr+1) {
			return lf, 1
		}
		if cr >= 0 {
			return cr, 1
		}
	}
	return -1, 0
}

// AddPriorityEntry records an approximate sparse anchor near the active viewport.
func (idx *Index) AddPriorityEntry(e Entry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	insertEntry(&idx.priorityEntries, e)
}

// ApproxLineToOffset returns the nearest known entry <= requested line.
func (idx *Index) ApproxLineToOffset(line int64) (Entry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	best, ok := floorEntryByLine(idx.entries, line)
	priorityBest, priorityOK := floorEntryByLine(idx.priorityEntries, line)
	switch {
	case !ok && !priorityOK:
		return Entry{}, false
	case !ok:
		return priorityBest, true
	case !priorityOK:
		return best, true
	case priorityBest.Line > best.Line:
		return priorityBest, true
	case priorityBest.Line == best.Line && priorityBest.Offset > best.Offset:
		return priorityBest, true
	default:
		return best, true
	}
}

// ApproxOffsetToLine estimates the line number at or before an offset.
func (idx *Index) ApproxOffsetToLine(offset int64) (Entry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	best, ok := floorEntryByOffset(idx.entries, offset)
	priorityBest, priorityOK := floorEntryByOffset(idx.priorityEntries, offset)
	switch {
	case !ok && !priorityOK:
		return Entry{}, false
	case !ok:
		best = priorityBest
	case priorityOK && priorityBest.Offset > best.Offset:
		best = priorityBest
	}

	estimated := best.Line
	if idx.bytes > 0 && idx.lines > 0 && offset > best.Offset {
		avgBytesPerLine := float64(idx.bytes) / float64(idx.lines+1)
		if avgBytesPerLine > 0 {
			estimated += int64(float64(offset-best.Offset) / avgBytesPerLine)
		}
	}
	if estimated < best.Line {
		estimated = best.Line
	}
	return Entry{Line: estimated, Offset: offset}, true
}

// FloorOffsetEntry returns the nearest exact sparse anchor at or before offset.
func (idx *Index) FloorOffsetEntry(offset int64) (Entry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	return floorEntryByOffset(idx.entries, offset)
}

func (idx *Index) addExact(e Entry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	insertEntry(&idx.entries, e)
}

func (idx *Index) setProgress(lines, bytes int64) {
	idx.mu.Lock()
	idx.lines = lines
	idx.bytes = bytes
	idx.mu.Unlock()
}

func (idx *Index) setDone(lines, bytes int64) {
	idx.mu.Lock()
	idx.lines = lines
	idx.bytes = bytes
	idx.done = true
	idx.priorityEntries = nil
	idx.mu.Unlock()
}

func floorEntryByLine(entries []Entry, line int64) (Entry, bool) {
	if len(entries) == 0 {
		return Entry{}, false
	}
	i := sort.Search(len(entries), func(i int) bool {
		return entries[i].Line > line
	})
	if i == 0 {
		return Entry{}, false
	}
	return entries[i-1], true
}

func floorEntryByOffset(entries []Entry, offset int64) (Entry, bool) {
	if len(entries) == 0 {
		return Entry{}, false
	}
	if offset <= 0 {
		return entries[0], true
	}
	i := sort.Search(len(entries), func(i int) bool {
		return entries[i].Offset > offset
	})
	if i == 0 {
		return entries[0], true
	}
	return entries[i-1], true
}

func insertEntry(dst *[]Entry, e Entry) {
	entries := *dst
	i := sort.Search(len(entries), func(i int) bool {
		if entries[i].Line != e.Line {
			return entries[i].Line >= e.Line
		}
		return entries[i].Offset >= e.Offset
	})
	if i < len(entries) && entries[i] == e {
		return
	}

	entries = append(entries, Entry{})
	copy(entries[i+1:], entries[i:])
	entries[i] = e
	*dst = entries
}
