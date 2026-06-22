package search

import (
	"context"
	"errors"
	"io"
	"regexp"

	"novera/internal/bigfile/regexutil"
)

const defaultRegexMatchWindow = 1 * 1024 * 1024

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
	re, err := compileRegexp(pattern, opts.CaseInsensitive)
	if err != nil {
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

	opts = normalizeRegexOptions(opts)
	size := r.Size()
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

// FindRegexpBackward scans forward up to opts.StartOffset and emits matches in descending order.
func FindRegexpBackward(ctx context.Context, r ReaderAtSize, re *regexp.Regexp, opts RegexOptions, emit func(Match) error) error {
	if re == nil {
		return errors.New("nil regexp")
	}

	opts = normalizeRegexOptions(opts)
	size := r.Size()
	endOffset := opts.StartOffset
	if endOffset <= 0 || endOffset > size {
		endOffset = size
	}

	hits := 0
	var matches []Match
	var window []byte // reused across chunks (grow-only)

	for off := int64(0); off < endOffset; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		primaryEnd := off + int64(opts.ChunkSize)
		if primaryEnd > endOffset {
			primaryEnd = endOffset
		}
		windowStart := off - int64(opts.MaxMatchWindow)
		if windowStart < 0 {
			windowStart = 0
		}
		windowEnd := primaryEnd + int64(opts.MaxMatchWindow)
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
		window = window[:readWindow]
		availableEnd := windowStart + int64(readWindow)
		if availableEnd < primaryEnd {
			primaryEnd = availableEnd
		}
		if primaryEnd <= off {
			break
		}

		locs := re.FindAllIndex(window, -1)
		for _, loc := range locs {
			absStart := windowStart + int64(loc[0])
			if absStart < off {
				continue
			}
			if absStart >= primaryEnd {
				break
			}
			matches = append(matches, Match{Offset: absStart, Length: loc[1] - loc[0]})
			hits++
			if opts.MaxHits > 0 && len(matches) > opts.MaxHits {
				copy(matches, matches[1:])
				matches = matches[:opts.MaxHits]
			}
		}

		off = primaryEnd

		if opts.Progress != nil {
			opts.Progress(Progress{
				BytesProcessed: off,
				BytesTotal:     endOffset,
				Matches:        int64(hits),
			})
		}
	}

	for i := len(matches) - 1; i >= 0; i-- {
		if err := emit(matches[i]); err != nil {
			return err
		}
	}
	return nil
}

func compileRegexp(pattern []byte, caseInsensitive bool) (*regexp.Regexp, error) {
	return regexutil.Compile(pattern, caseInsensitive)
}

func normalizeRegexOptions(opts RegexOptions) RegexOptions {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 4 * 1024 * 1024
	}
	if opts.MaxMatchWindow <= 0 {
		opts.MaxMatchWindow = defaultRegexMatchWindow
	}
	if opts.MaxMatchWindow > opts.ChunkSize {
		opts.MaxMatchWindow = opts.ChunkSize
	}
	return opts
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
