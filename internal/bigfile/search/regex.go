package search

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"

	"novera/internal/bigfile/regexutil"
)

const defaultRegexMatchWindow = 1 * 1024 * 1024
const maxBackwardRegexHits = MaxCollectedHits
const maxRegexChunkSize = MaxChunkBytes

// RegexOptions controls chunked regex search.
type RegexOptions struct {
	ChunkSize       int
	MaxHits         int
	StartOffset     int64
	Backward        bool
	CaseInsensitive bool
	MaxMatchWindow  int
	Progress        func(Progress)
}

// CollectRegexp returns up to opts.MaxHits regex matches with previews.
func CollectRegexp(ctx context.Context, r ReaderAtSize, pattern []byte, opts RegexOptions, previewBytes int) ([]Result, error) {
	if err := validateRegexOptions(opts); err != nil {
		return nil, err
	}
	if err := validateCollectRequest(len(pattern), opts.MaxHits, previewBytes); err != nil {
		return nil, err
	}
	opts = normalizeRegexOptions(opts)
	if err := validateRegexOptions(opts); err != nil {
		return nil, err
	}
	re, analysis, err := regexutil.CompileBounded(pattern, opts.CaseInsensitive, opts.MaxMatchWindow)
	if err != nil {
		return nil, err
	}
	if err := validateCollectRequest(int(analysis.MaxMatchBytes), opts.MaxHits, previewBytes); err != nil {
		return nil, err
	}

	var results []Result
	find := FindRegexp
	if opts.Backward {
		find = FindRegexpBackward
	}
	err = find(ctx, r, re, opts, func(m Match) error {
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

// FindRegexp scans a file-like object in chunks and emits regex matches.
func FindRegexp(ctx context.Context, r ReaderAtSize, re *regexp.Regexp, opts RegexOptions, emit func(Match) error) error {
	if re == nil {
		return errors.New("nil regexp")
	}

	if err := validateRegexOptions(opts); err != nil {
		return err
	}
	opts = normalizeRegexOptions(opts)
	if err := regexutil.ValidateCompiledBounded(re, opts.MaxMatchWindow); err != nil {
		return err
	}
	size := r.Size()
	if size < 0 {
		return errors.New("source size must not be negative")
	}
	startOffset := clampOffset(opts.StartOffset, size)
	hits := 0
	var window []byte // reused across chunks (grow-only), like the plain path

	for off := startOffset; off < size; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		primaryEnd := off + int64(opts.ChunkSize)
		if primaryEnd > size {
			primaryEnd = size
		}
		windowStart := off - int64(opts.MaxMatchWindow)
		if windowStart < 0 {
			windowStart = 0
		}
		windowEnd := primaryEnd + int64(opts.MaxMatchWindow)
		if windowEnd > size {
			windowEnd = size
		}
		need := windowEnd - windowStart
		if int64(cap(window)) < need {
			window = make([]byte, need)
		}
		window = window[:need]
		readWindow, windowErr := r.ReadAt(window, windowStart)
		if windowErr != nil && !errors.Is(windowErr, io.EOF) {
			return windowErr
		}
		if readWindow != len(window) {
			return io.ErrUnexpectedEOF
		}
		window = window[:readWindow]
		availableEnd := windowStart + int64(readWindow)
		if availableEnd < primaryEnd {
			primaryEnd = availableEnd
		}
		if primaryEnd <= off {
			break
		}

		// Moving cursor instead of FindAllIndex(window, -1): stop as soon as we hit
		// MaxHits (FindNext uses MaxHits=1) instead of finding every match in the
		// whole window first.
		reachedMaxHits := false
		for pos := 0; pos <= len(window); {
			loc := re.FindIndex(window[pos:])
			if loc == nil {
				break
			}
			mStart, mEnd := loc[0]+pos, loc[1]+pos
			absStart := windowStart + int64(mStart)
			if absStart >= primaryEnd {
				break
			}
			if absStart >= off {
				if err := emit(Match{Offset: absStart, Length: mEnd - mStart}); err != nil {
					return err
				}
				hits++
				if opts.MaxHits > 0 && hits >= opts.MaxHits {
					reachedMaxHits = true
					break
				}
			}
			if loc[1] > loc[0] {
				pos = mEnd
			} else {
				pos = mEnd + 1 // zero-width match: advance to avoid an infinite loop
			}
		}

		off = primaryEnd

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

// FindRegexpBackward scans from opts.StartOffset toward the beginning and emits
// matches in descending order. A positive MaxHits bounds the retained tail for
// each chunk; an unbounded request fails if one chunk exceeds the fixed safety
// ceiling instead of materializing every match in a dense window.
func FindRegexpBackward(ctx context.Context, r ReaderAtSize, re *regexp.Regexp, opts RegexOptions, emit func(Match) error) error {
	if re == nil {
		return errors.New("nil regexp")
	}

	if err := validateRegexOptions(opts); err != nil {
		return err
	}
	opts = normalizeRegexOptions(opts)
	if err := regexutil.ValidateCompiledBounded(re, opts.MaxMatchWindow); err != nil {
		return err
	}
	if opts.MaxHits > maxBackwardRegexHits {
		return errors.New("maximum hits exceeds the backward-search safety limit")
	}
	size := r.Size()
	if size < 0 {
		return errors.New("source size must not be negative")
	}
	endOffset := opts.StartOffset
	if endOffset <= 0 || endOffset > size {
		endOffset = size
	}

	hits := 0
	var window []byte // reused across chunks (grow-only)

	var processed int64
	for end := endOffset; end > 0; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		chunkStart := end - int64(opts.ChunkSize)
		if chunkStart < 0 {
			chunkStart = 0
		}
		windowStart := chunkStart - int64(opts.MaxMatchWindow)
		if windowStart < 0 {
			windowStart = 0
		}
		windowEnd := end + int64(opts.MaxMatchWindow)
		if windowEnd > endOffset {
			windowEnd = endOffset
		}
		need := windowEnd - windowStart
		if int64(cap(window)) < need {
			window = make([]byte, need)
		}
		window = window[:need]
		readWindow, windowErr := r.ReadAt(window, windowStart)
		if windowErr != nil && !errors.Is(windowErr, io.EOF) {
			return windowErr
		}
		if readWindow != len(window) {
			return io.ErrUnexpectedEOF
		}
		window = window[:readWindow]
		remaining := maxBackwardRegexHits
		if opts.MaxHits > 0 {
			remaining = opts.MaxHits - hits
		}
		matches := make([]Match, 0, remaining)
		ringStart := 0
		for pos := 0; pos <= len(window); {
			loc := re.FindIndex(window[pos:])
			if loc == nil {
				break
			}
			matchStart, matchEnd := pos+loc[0], pos+loc[1]
			absStart := windowStart + int64(matchStart)
			if absStart < chunkStart {
				// The match belongs to the preceding chunk.
			} else if absStart >= end {
				break
			} else if len(matches) < remaining {
				matches = append(matches, Match{Offset: absStart, Length: matchEnd - matchStart})
			} else if opts.MaxHits == 0 {
				return errors.New("backward regex search exceeded the safe per-chunk match limit; specify MaxHits")
			} else {
				matches[ringStart] = Match{Offset: absStart, Length: matchEnd - matchStart}
				ringStart = (ringStart + 1) % len(matches)
			}

			if loc[1] > loc[0] {
				pos = matchEnd
			} else {
				pos = matchEnd + 1
			}
		}

		for i := range slices.Backward(matches) {
			index := i
			if ringStart != 0 {
				index = (ringStart + i) % len(matches)
			}
			if err := emit(matches[index]); err != nil {
				return err
			}
			hits++
			if opts.MaxHits > 0 && hits >= opts.MaxHits {
				break
			}
		}

		processed += end - chunkStart
		end = chunkStart

		if opts.Progress != nil {
			opts.Progress(Progress{
				BytesProcessed: processed,
				BytesTotal:     endOffset,
				Matches:        int64(hits),
			})
		}
		if opts.MaxHits > 0 && hits >= opts.MaxHits {
			return nil
		}
	}

	return nil
}

func compileRegexp(pattern []byte, caseInsensitive bool) (*regexp.Regexp, error) {
	return regexutil.Compile(pattern, caseInsensitive)
}

func normalizeRegexOptions(opts RegexOptions) RegexOptions {
	if opts.ChunkSize == 0 {
		opts.ChunkSize = 4 * 1024 * 1024
	}
	if opts.MaxMatchWindow == 0 {
		opts.MaxMatchWindow = defaultRegexMatchWindow
	}
	if opts.MaxMatchWindow > opts.ChunkSize {
		opts.MaxMatchWindow = opts.ChunkSize
	}
	return opts
}

func validateRegexOptions(opts RegexOptions) error {
	if opts.ChunkSize < 0 {
		return fmt.Errorf("%w: search chunk size must not be negative", regexutil.ErrRegexResourceLimit)
	}
	if opts.ChunkSize > maxRegexChunkSize {
		return fmt.Errorf("%w: search chunk %d exceeds %d bytes", regexutil.ErrRegexResourceLimit, opts.ChunkSize, maxRegexChunkSize)
	}
	if opts.MaxMatchWindow < 0 {
		return fmt.Errorf("%w: regex match window must not be negative", regexutil.ErrRegexResourceLimit)
	}
	if opts.MaxMatchWindow > regexutil.MaxExactMatchWindowBytes {
		return fmt.Errorf("%w: match window %d exceeds %d bytes", regexutil.ErrRegexResourceLimit, opts.MaxMatchWindow, regexutil.MaxExactMatchWindowBytes)
	}
	if opts.MaxHits < 0 {
		return searchLimit("maximum hits must not be negative")
	}
	return nil
}

func clampOffset(offset int64, size int64) int64 {
	if offset < 0 {
		return 0
	}
	if offset > size {
		return size
	}
	return offset
}

// CompileRegexpForTesting exposes regex validation to tests in sibling packages.
func CompileRegexpForTesting(pattern []byte, caseInsensitive bool) (*regexp.Regexp, error) {
	return compileRegexp(pattern, caseInsensitive)
}
