package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"novera/internal/bigfile/asciifold"
)

// Match is a search result at a byte offset.
type Match struct {
	Offset int64
	Length int
}

// Result is a search match with a bounded preview snippet.
type Result struct {
	Match
	PreviewStart int64
	Preview      string
}

// Progress describes a long-running search operation.
type Progress struct {
	BytesProcessed int64
	BytesTotal     int64
	Matches        int64
}

// ReaderAtSize is the file interface needed for chunked search.
type ReaderAtSize interface {
	io.ReaderAt
	Size() int64
}

// PlainOptions controls chunked plain-text search.
type PlainOptions struct {
	ChunkSize       int
	MaxHits         int
	StartOffset     int64
	Backward        bool
	CaseInsensitive bool
	WholeWord       bool
	// ByteAlignment restricts candidate starts to source-code-unit boundaries.
	// Use 2 for UTF-16LE/BE and 1 (the default) for UTF-8/single-byte data.
	ByteAlignment int
	Progress      func(Progress)
}

// CollectPlain returns up to opts.MaxHits plain-text matches with previews.
func CollectPlain(ctx context.Context, r ReaderAtSize, pattern []byte, opts PlainOptions, previewBytes int) ([]Result, error) {
	if err := validatePlainPattern(pattern); err != nil {
		return nil, err
	}
	if err := validateStreamingOptions(opts.ChunkSize, opts.MaxHits); err != nil {
		return nil, err
	}
	if err := validateCollectRequest(len(pattern), opts.MaxHits, previewBytes); err != nil {
		return nil, err
	}
	var results []Result
	find := FindPlain
	if opts.Backward {
		find = FindPlainBackward
	}
	err := find(ctx, r, pattern, opts, func(m Match) error {
		preview, start, err := previewAt(r, m.Offset, m.Length, previewBytes)
		if err != nil {
			return err
		}
		results = append(results, Result{
			Match:        m,
			PreviewStart: start,
			Preview:      preview,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// FindPlain scans a file-like object in chunks and emits matches.
// It handles matches crossing chunk boundaries by carrying len(pattern)-1 bytes.
func FindPlain(ctx context.Context, r ReaderAtSize, pattern []byte, opts PlainOptions, emit func(Match) error) error {
	if err := validatePlainPattern(pattern); err != nil {
		return err
	}
	if err := validateStreamingOptions(opts.ChunkSize, opts.MaxHits); err != nil {
		return err
	}
	if err := validatePlainAlignment(pattern, opts); err != nil {
		return err
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize = 32 * 1024 * 1024
	}
	if opts.WholeWord && opts.ChunkSize < len(pattern)+utf8.UTFMax {
		opts.ChunkSize = len(pattern) + utf8.UTFMax
	}

	size := r.Size()
	if size < 0 {
		return errors.New("source size must not be negative")
	}
	startOffset := opts.StartOffset
	if startOffset < 0 {
		startOffset = 0
	}
	if startOffset > size {
		startOffset = size
	}
	buf := make([]byte, opts.ChunkSize)
	keepSize := len(pattern) - 1
	if opts.WholeWord {
		// A boundary rune may occupy four UTF-8 bytes. Retain a complete rune
		// on both sides so a chunk seam is never treated as a safe non-word
		// boundary merely because DecodeRune sees an incomplete sequence.
		keepSize = len(pattern) + 2*utf8.UTFMax
	}
	carry := make([]byte, 0, keepSize)
	window := make([]byte, 0, opts.ChunkSize+keepSize)
	needle := pattern
	if opts.CaseInsensitive {
		needle = asciifold.Fold(pattern)
	}

	off := startOffset - int64(keepSize)
	if off < 0 {
		off = 0
	}
	hits := 0
	lastEmitted := int64(-1)

	for off < size {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		want := opts.ChunkSize
		if remaining := size - off; remaining < int64(want) {
			want = int(remaining)
		}

		n, err := r.ReadAt(buf[:want], off)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n != want {
			return io.ErrUnexpectedEOF
		}
		if n == 0 {
			break
		}

		windowStart := off - int64(len(carry))
		window = window[:0]
		window = append(window, carry...)
		window = append(window, buf[:n]...)
		// Emit matches that either start in the current chunk or start in the
		// previous carry but finish in the current chunk. Matches that ended at
		// or before 'off' were already discoverable in the previous iteration.

		searchFrom := 0
		reachedMaxHits := false
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			idx := indexPlain(window[searchFrom:], needle, opts.CaseInsensitive)
			if idx < 0 {
				break
			}
			pos := searchFrom + idx
			abs := windowStart + int64(pos)

			knownInThisWindow := abs+int64(len(pattern)) > off
			if opts.WholeWord {
				// A complete pattern may precede an incomplete delimiter rune at
				// the previous chunk's end. Revisit retained candidates until that
				// rune is available, deduplicating by the last emitted offset.
				knownInThisWindow = abs > lastEmitted
			}
			if abs >= startOffset && plainMatchAligned(abs, opts.ByteAlignment) && knownInThisWindow && wordBoundaryOK(window, pos, len(pattern), windowStart, size, opts.WholeWord) {
				if err := emit(Match{Offset: abs, Length: len(pattern)}); err != nil {
					return err
				}
				hits++
				lastEmitted = abs
				if opts.MaxHits > 0 && hits >= opts.MaxHits {
					reachedMaxHits = true
					break
				}
			}
			searchFrom = pos + 1
		}

		keep := keepSize
		if keep > len(window) {
			keep = len(window)
		}
		carry = append(carry[:0], window[len(window)-keep:]...)

		off += int64(n)
		if opts.Progress != nil {
			opts.Progress(Progress{
				BytesProcessed: off,
				BytesTotal:     size,
				Matches:        int64(hits),
			})
		}
		if reachedMaxHits {
			return nil
		}
	}

	return nil
}

// FindPlainBackward scans a file-like object from the end toward the beginning
// and emits matches in descending byte-offset order.
func FindPlainBackward(ctx context.Context, r ReaderAtSize, pattern []byte, opts PlainOptions, emit func(Match) error) error {
	if err := validatePlainPattern(pattern); err != nil {
		return err
	}
	if err := validateStreamingOptions(opts.ChunkSize, opts.MaxHits); err != nil {
		return err
	}
	if err := validatePlainAlignment(pattern, opts); err != nil {
		return err
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize = 32 * 1024 * 1024
	}
	if opts.WholeWord && opts.ChunkSize < len(pattern)+utf8.UTFMax {
		opts.ChunkSize = len(pattern) + utf8.UTFMax
	}

	size := r.Size()
	if size < 0 {
		return errors.New("source size must not be negative")
	}
	end := opts.StartOffset
	if end <= 0 || end > size {
		end = size
	}
	totalToScan := end

	keepSize := len(pattern) - 1
	if opts.WholeWord {
		keepSize = len(pattern) + utf8.UTFMax
	}
	needle := pattern
	if opts.CaseInsensitive {
		needle = asciifold.Fold(pattern)
	}
	buf := make([]byte, opts.ChunkSize+2*keepSize)
	var processed int64
	hits := 0
	for end > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		chunkStart := end - int64(opts.ChunkSize)
		if chunkStart < 0 {
			chunkStart = 0
		}
		readStart := chunkStart - int64(keepSize)
		if readStart < 0 {
			readStart = 0
		}
		readEnd := end + int64(keepSize)
		if readEnd > size {
			readEnd = size
		}

		want := int(readEnd - readStart)
		if want > len(buf) {
			buf = make([]byte, want)
		}
		n, err := r.ReadAt(buf[:want], readStart)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n != want {
			return io.ErrUnexpectedEOF
		}
		window := buf[:n]
		searchEnd := len(window)
		reachedMaxHits := false
		for searchEnd >= len(needle) {
			if err := ctx.Err(); err != nil {
				return err
			}
			pos := lastIndexPlain(window[:searchEnd], needle, opts.CaseInsensitive)
			if pos < 0 {
				break
			}
			abs := readStart + int64(pos)
			if abs >= chunkStart && abs < end && plainMatchAligned(abs, opts.ByteAlignment) && wordBoundaryOK(window, pos, len(pattern), readStart, size, opts.WholeWord) {
				if err := emit(Match{Offset: abs, Length: len(pattern)}); err != nil {
					return err
				}
				hits++
				if opts.MaxHits > 0 && hits >= opts.MaxHits {
					reachedMaxHits = true
					break
				}
			}
			// Preserve overlapping matches while guaranteeing that the next
			// search considers only candidates whose start is before pos.
			searchEnd = pos + len(needle) - 1
		}

		if reachedMaxHits {
			processed += end - chunkStart
			if opts.Progress != nil {
				opts.Progress(Progress{BytesProcessed: processed, BytesTotal: totalToScan, Matches: int64(hits)})
			}
			return nil
		}

		processed += end - chunkStart
		if opts.Progress != nil {
			opts.Progress(Progress{
				BytesProcessed: processed,
				BytesTotal:     totalToScan,
				Matches:        int64(hits),
			})
		}

		end = chunkStart
	}

	return nil
}

func indexPlain(window []byte, needle []byte, caseInsensitive bool) int {
	if caseInsensitive {
		return asciifold.IndexFolded(window, needle)
	}
	return bytes.Index(window, needle)
}

func lastIndexPlain(window []byte, needle []byte, caseInsensitive bool) int {
	if caseInsensitive {
		return asciifold.LastIndexFolded(window, needle)
	}
	return bytes.LastIndex(window, needle)
}

func validatePlainAlignment(pattern []byte, opts PlainOptions) error {
	alignment := opts.ByteAlignment
	if alignment == 0 || alignment == 1 {
		return nil
	}
	if alignment != 2 {
		return errors.New("plain-search byte alignment must be 1 or 2")
	}
	if len(pattern)%alignment != 0 {
		return errors.New("plain-search pattern is not aligned to the source code-unit width")
	}
	if opts.WholeWord {
		return errors.New("whole-word search is unsupported for fixed-width encoded bytes")
	}
	return nil
}

func plainMatchAligned(offset int64, alignment int) bool {
	return alignment <= 1 || offset%int64(alignment) == 0
}

func wordBoundaryOK(window []byte, pos int, length int, windowStart int64, size int64, wholeWord bool) bool {
	if !wholeWord {
		return true
	}
	beforeOK := pos == 0 && windowStart == 0
	if pos > 0 {
		beforeOK = !isWordRuneBefore(window[:pos])
	}

	after := pos + length
	afterOK := after >= len(window) && windowStart+int64(after) >= size
	if after < len(window) {
		afterOK = !isWordRuneAt(window[after:])
	}

	return beforeOK && afterOK
}

func isWordRuneBefore(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	r, width := utf8.DecodeLastRune(data)
	if r == utf8.RuneError && width == 1 {
		// Invalid or incomplete input is not proof of a boundary. Refuse the
		// whole-word candidate instead of allowing a false positive.
		return true
	}
	return isWordRune(r)
}

func isWordRuneAt(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	r, width := utf8.DecodeRune(data)
	if r == utf8.RuneError && width == 1 {
		return true
	}
	return isWordRune(r)
}

func isWordRune(r rune) bool {
	// Combining marks continue a word even though they are not letters by
	// themselves. This keeps decomposed text such as "e\u0301" from exposing
	// a false whole-word match for the base letter.
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}

func previewAt(r ReaderAtSize, offset int64, length int, radius int) (string, int64, error) {
	if radius < 0 {
		return "", 0, searchLimit("preview bytes must not be negative")
	}
	if radius > MaxPreviewBytes {
		return "", 0, searchLimit("preview radius %d exceeds %d bytes", radius, MaxPreviewBytes)
	}
	start := offset - int64(radius)
	if start < 0 {
		start = 0
	}
	end := offset + int64(length+radius)
	if size := r.Size(); end > size {
		end = size
	}
	if end < start {
		end = start
	}

	buf := make([]byte, end-start)
	n, err := r.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", start, err
	}
	return cleanPreview(buf[:n]), start, nil
}

func cleanPreview(buf []byte) string {
	if !utf8.Valid(buf) {
		return fmt.Sprintf("[%d preview bytes]", len(buf))
	}
	var out strings.Builder
	out.Grow(len(buf))
	for field := range strings.FieldsSeq(string(buf)) {
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(field)
	}
	return out.String()
}
