package manualedit

import (
	"context"
	"errors"
	"fmt"
	"io"

	"novera/internal/bigfile/document"
)

const DefaultMaxInsertedBytes int64 = 8 * 1024 * 1024

var ErrInsertedTextTooLarge = errors.New("inserted text exceeds the safe edit limit")

type Range struct {
	Start int64
	End   int64
}

type Edit struct {
	Start int64
	End   int64
	Text  []byte
}

type Progress struct {
	BytesWritten int64
	BytesTotal   int64
}

type WriteOptions struct {
	Progress func(Progress)
}

type pieceSource uint8

const (
	pieceOriginal pieceSource = iota
	pieceAdded
)

type piece struct {
	source pieceSource
	start  int64
	length int64
}

// PieceTable is mutable and intentionally unsynchronized. It is safe as a
// single-owner staging structure; concurrent mutation/readers need an external
// lock or immutable snapshot handoff.
type PieceTable struct {
	originalSize int64
	size         int64
	maxInserted  int64
	added        []byte
	pieces       []piece
	modified     []Range
}

func NewPieceTable(size int64) *PieceTable {
	pt := &PieceTable{
		originalSize: size,
		size:         size,
		maxInserted:  DefaultMaxInsertedBytes,
	}
	if size > 0 {
		pt.pieces = []piece{{source: pieceOriginal, start: 0, length: size}}
	}
	return pt
}

func (pt *PieceTable) SetMaxInsertedBytes(limit int64) {
	if limit > 0 {
		pt.maxInserted = limit
	}
}

func (pt *PieceTable) Size() int64 {
	return pt.size
}

// OriginalSize is the source size captured when the table was built. Comparing
// it to the source's current size before a save detects a source that changed
// (shrank/grew/was replaced) since staging began.
func (pt *PieceTable) OriginalSize() int64 {
	return pt.originalSize
}

func (pt *PieceTable) ModifiedRanges() []Range {
	out := make([]Range, len(pt.modified))
	copy(out, pt.modified)
	return out
}

func (pt *PieceTable) Replace(start int64, end int64, text []byte) error {
	if pt == nil {
		return errors.New("piece table is required")
	}
	if start < 0 || end < 0 {
		return errors.New("edit offsets must be non-negative")
	}
	if end < start {
		return errors.New("end offset must be greater than or equal to start offset")
	}
	if start > pt.size || end > pt.size {
		return errors.New("edit range is beyond end of document")
	}
	if pt.maxInserted > 0 && int64(len(text)) > pt.maxInserted {
		return ErrInsertedTextTooLarge
	}

	insertStart := int64(len(pt.added))
	if len(text) > 0 {
		pt.added = append(pt.added, text...)
	}

	newPieces := make([]piece, 0, len(pt.pieces)+2)
	cursor := int64(0)
	inserted := false

	for _, current := range pt.pieces {
		pieceStart := cursor
		pieceEnd := cursor + current.length

		if pieceEnd <= start || pieceStart >= end {
			if !inserted && pieceStart >= end {
				newPieces = appendInsertedPiece(newPieces, insertStart, int64(len(text)))
				inserted = true
			}
			newPieces = append(newPieces, current)
			cursor = pieceEnd
			continue
		}

		if pieceStart < start {
			newPieces = append(newPieces, piece{
				source: current.source,
				start:  current.start,
				length: start - pieceStart,
			})
		}
		if !inserted {
			newPieces = appendInsertedPiece(newPieces, insertStart, int64(len(text)))
			inserted = true
		}
		if pieceEnd > end {
			skip := end - pieceStart
			newPieces = append(newPieces, piece{
				source: current.source,
				start:  current.start + skip,
				length: pieceEnd - end,
			})
		}

		cursor = pieceEnd
	}

	if !inserted {
		newPieces = appendInsertedPiece(newPieces, insertStart, int64(len(text)))
	}

	pt.pieces = normalizePieces(newPieces)
	pt.size = pt.size - (end - start) + int64(len(text))
	pt.recordModifiedRange(start, max64(end, start+int64(len(text))))
	return nil
}

func (pt *PieceTable) WriteTo(ctx context.Context, src document.ReaderAtSize, dst syncWriter, opts WriteOptions) (int64, error) {
	if pt == nil {
		return 0, errors.New("piece table is required")
	}
	if src == nil {
		return 0, errors.New("source document is required")
	}
	if dst == nil {
		return 0, errors.New("destination is required")
	}

	total := pt.size
	var written int64
	buf := make([]byte, 1024*1024)

	for _, current := range pt.pieces {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}

		switch current.source {
		case pieceOriginal:
			reader := io.NewSectionReader(src, current.start, current.length)
			var pieceWritten int64
			for {
				select {
				case <-ctx.Done():
					return written, ctx.Err()
				default:
				}

				n, readErr := reader.Read(buf)
				if n > 0 {
					w, writeErr := dst.Write(buf[:n])
					written += int64(w)
					pieceWritten += int64(w)
					if opts.Progress != nil {
						opts.Progress(Progress{BytesWritten: written, BytesTotal: total})
					}
					if writeErr != nil {
						return written, writeErr
					}
					if w != n {
						return written, io.ErrShortWrite
					}
				}
				if errors.Is(readErr, io.EOF) {
					break
				}
				if readErr != nil {
					return written, readErr
				}
			}
			// A short read means the source shrank/was replaced since staging, so
			// the offsets we're copying no longer reference the bytes we expect.
			// Fail loudly instead of silently writing a truncated/misaligned copy.
			if pieceWritten != current.length {
				return written, fmt.Errorf("source shrank during save (read %d of %d bytes at offset %d): %w",
					pieceWritten, current.length, current.start, ErrSourceModifiedDuringOperation)
			}
		case pieceAdded:
			start := current.start
			end := current.start + current.length
			for pos := start; pos < end; {
				select {
				case <-ctx.Done():
					return written, ctx.Err()
				default:
				}

				chunkEnd := pos + int64(len(buf))
				if chunkEnd > end {
					chunkEnd = end
				}
				w, err := dst.Write(pt.added[pos:chunkEnd])
				written += int64(w)
				if opts.Progress != nil {
					opts.Progress(Progress{BytesWritten: written, BytesTotal: total})
				}
				if err != nil {
					return written, err
				}
				if int64(w) != chunkEnd-pos {
					return written, io.ErrShortWrite
				}
				pos = chunkEnd
			}
		default:
			return written, errors.New("unknown piece source")
		}
	}

	return written, dst.Sync()
}

// ReadRange returns the transformed (edited) bytes in [start, end), reading
// original spans from src and added spans from the staging buffer. Used to
// render a window that reflects staged edits.
func (pt *PieceTable) ReadRange(src document.ReaderAtSize, start int64, end int64) ([]byte, error) {
	if pt == nil {
		return nil, errors.New("piece table is required")
	}
	if src == nil {
		return nil, errors.New("source document is required")
	}
	start = clamp64(start, 0, pt.size)
	end = clamp64(end, 0, pt.size)
	if end <= start {
		return []byte{}, nil
	}
	out := make([]byte, 0, end-start)
	cursor := int64(0)
	for _, current := range pt.pieces {
		pieceStart := cursor
		pieceEnd := cursor + current.length
		cursor = pieceEnd
		if pieceEnd <= start || pieceStart >= end {
			continue
		}
		within := max64(start, pieceStart) - pieceStart
		n := min64(end, pieceEnd) - max64(start, pieceStart)
		switch current.source {
		case pieceOriginal:
			buf := make([]byte, n)
			if _, err := src.ReadAt(buf, current.start+within); err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			out = append(out, buf...)
		case pieceAdded:
			from := current.start + within
			out = append(out, pt.added[from:from+n]...)
		default:
			return nil, errors.New("unknown piece source")
		}
	}
	return out, nil
}

func appendInsertedPiece(pieces []piece, start int64, length int64) []piece {
	if length <= 0 {
		return pieces
	}
	return append(pieces, piece{source: pieceAdded, start: start, length: length})
}

func normalizePieces(in []piece) []piece {
	out := make([]piece, 0, len(in))
	for _, current := range in {
		if current.length <= 0 {
			continue
		}
		if len(out) == 0 {
			out = append(out, current)
			continue
		}
		last := &out[len(out)-1]
		if last.source == current.source && last.start+last.length == current.start {
			last.length += current.length
			continue
		}
		out = append(out, current)
	}
	return out
}

func (pt *PieceTable) recordModifiedRange(start int64, end int64) {
	if end < start {
		end = start
	}
	next := Range{Start: start, End: end}
	if len(pt.modified) == 0 {
		pt.modified = append(pt.modified, next)
		return
	}

	merged := make([]Range, 0, len(pt.modified)+1)
	inserted := false
	for _, current := range pt.modified {
		if next.End < current.Start {
			if !inserted {
				merged = append(merged, next)
				inserted = true
			}
			merged = append(merged, current)
			continue
		}
		if current.End < next.Start {
			merged = append(merged, current)
			continue
		}
		next.Start = min64(next.Start, current.Start)
		next.End = max64(next.End, current.End)
	}
	if !inserted {
		merged = append(merged, next)
	}
	pt.modified = merged
}

func (pt *PieceTable) sourceRangeForTransformedRange(start int64, end int64) Range {
	if pt == nil {
		return Range{}
	}
	if end < start {
		end = start
	}
	start = clamp64(start, 0, pt.size)
	end = clamp64(end, 0, pt.size)
	if start == end {
		anchor := pt.sourceOffsetForTransformedPosition(start)
		return Range{Start: anchor, End: anchor}
	}

	var out Range
	foundOriginal := false
	cursor := int64(0)
	for _, current := range pt.pieces {
		pieceStart := cursor
		pieceEnd := cursor + current.length
		if current.source == pieceOriginal && start < pieceEnd && end > pieceStart {
			overlapStart := max64(start, pieceStart)
			overlapEnd := min64(end, pieceEnd)
			sourceStart := current.start + (overlapStart - pieceStart)
			sourceEnd := current.start + (overlapEnd - pieceStart)
			if !foundOriginal {
				out = Range{Start: sourceStart, End: sourceEnd}
				foundOriginal = true
			} else {
				out.Start = min64(out.Start, sourceStart)
				out.End = max64(out.End, sourceEnd)
			}
		}
		cursor = pieceEnd
	}
	if foundOriginal {
		return out
	}

	anchor := pt.sourceOffsetForTransformedPosition(start)
	return Range{Start: anchor, End: anchor}
}

func (pt *PieceTable) sourceRangeToTransformedRange(start int64, end int64) (Range, bool) {
	if pt == nil {
		return Range{}, false
	}
	if end < start {
		end = start
	}
	start = clamp64(start, 0, pt.originalSize)
	end = clamp64(end, 0, pt.originalSize)
	if start == end {
		pos, ok := pt.transformedPositionForSourceOffset(start)
		if !ok {
			return Range{}, false
		}
		return Range{Start: pos, End: pos}, true
	}

	var out Range
	var covered int64
	foundOriginal := false
	cursor := int64(0)
	for _, current := range pt.pieces {
		pieceStart := cursor
		pieceEnd := cursor + current.length
		if current.source == pieceOriginal && start < current.start+current.length && end > current.start {
			sourceStart := max64(start, current.start)
			sourceEnd := min64(end, current.start+current.length)
			transformedStart := pieceStart + (sourceStart - current.start)
			transformedEnd := pieceStart + (sourceEnd - current.start)
			if !foundOriginal {
				out = Range{Start: transformedStart, End: transformedEnd}
				foundOriginal = true
			} else {
				out.Start = min64(out.Start, transformedStart)
				out.End = max64(out.End, transformedEnd)
			}
			covered += sourceEnd - sourceStart
		}
		cursor = pieceEnd
	}
	if !foundOriginal || covered != end-start {
		return Range{}, false
	}
	return out, true
}

func (pt *PieceTable) transformedPositionForSourceOffset(sourceOffset int64) (int64, bool) {
	if pt == nil {
		return 0, false
	}
	sourceOffset = clamp64(sourceOffset, 0, pt.originalSize)
	cursor := int64(0)
	for _, current := range pt.pieces {
		pieceStart := cursor
		pieceEnd := cursor + current.length
		if current.source == pieceOriginal && sourceOffset >= current.start && sourceOffset <= current.start+current.length {
			return pieceStart + (sourceOffset - current.start), true
		}
		cursor = pieceEnd
	}
	if sourceOffset == pt.originalSize {
		return pt.size, true
	}
	return 0, false
}

func (pt *PieceTable) sourceOffsetForTransformedPosition(pos int64) int64 {
	if pt == nil {
		return 0
	}
	pos = clamp64(pos, 0, pt.size)
	cursor := int64(0)
	previousOriginalEnd := int64(0)
	for idx, current := range pt.pieces {
		pieceStart := cursor
		pieceEnd := cursor + current.length
		if current.source == pieceOriginal {
			if pos >= pieceStart && pos <= pieceEnd {
				return current.start + clamp64(pos-pieceStart, 0, current.length)
			}
			previousOriginalEnd = current.start + current.length
		} else if pos >= pieceStart && pos <= pieceEnd {
			if previousOriginalEnd > 0 || pieceStart == 0 {
				return previousOriginalEnd
			}
			if next, ok := nextOriginalStart(pt.pieces[idx+1:]); ok {
				return next
			}
			return previousOriginalEnd
		}
		cursor = pieceEnd
	}
	return pt.originalSize
}

func nextOriginalStart(pieces []piece) (int64, bool) {
	for _, current := range pieces {
		if current.source == pieceOriginal {
			return current.start, true
		}
	}
	return 0, false
}

func clamp64(value int64, low int64, high int64) int64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func min64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

type syncWriter interface {
	io.Writer
	Sync() error
}
