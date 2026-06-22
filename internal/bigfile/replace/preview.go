package replace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"novera/internal/bigfile/search"
)

// Preview describes one bounded replacement preview.
type Preview struct {
	Offset int64
	Before string
	After  string
}

// PreviewOptions controls plain replace previews.
type PreviewOptions struct {
	ChunkSize       int
	MaxHits         int
	PreviewBytes    int
	CaseInsensitive bool
	WholeWord       bool
}

// RegexPreviewOptions controls regex replace previews.
//
// Regex-specific matching flags live in RegexOptions. Plain-only preview flags
// such as WholeWord are intentionally not exposed on regex preview paths.
type RegexPreviewOptions struct {
	ChunkSize    int
	MaxHits      int
	PreviewBytes int
}

// ReaderAtSize is the file interface needed for replace preview.
type ReaderAtSize interface {
	io.ReaderAt
	Size() int64
}

// PreviewPlain returns bounded before/after snippets for the first matches.
func PreviewPlain(ctx context.Context, r ReaderAtSize, pattern []byte, repl []byte, opts PreviewOptions) ([]Preview, error) {
	if len(pattern) == 0 {
		return nil, errors.New("empty pattern")
	}
	radius := opts.PreviewBytes
	if radius <= 0 {
		radius = 48
	}

	results, err := search.CollectPlain(ctx, r, pattern, search.PlainOptions{
		ChunkSize:       opts.ChunkSize,
		MaxHits:         opts.MaxHits,
		CaseInsensitive: opts.CaseInsensitive,
		WholeWord:       opts.WholeWord,
	}, radius)
	if err != nil {
		return nil, err
	}

	previews := make([]Preview, 0, len(results))
	for _, result := range results {
		before, after, err := replacementSnippet(r, result.Offset, result.Length, repl, radius)
		if err != nil {
			return nil, err
		}
		previews = append(previews, Preview{
			Offset: result.Offset,
			Before: before,
			After:  after,
		})
	}
	return previews, nil
}

func replacementSnippet(r ReaderAtSize, offset int64, length int, repl []byte, radius int) (string, string, error) {
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
		return "", "", err
	}
	buf = buf[:n]

	localStart := int(offset - start)
	localEnd := localStart + length
	if localStart < 0 || localEnd > len(buf) {
		return "", "", errors.New("preview window does not contain match")
	}

	after := bytes.NewBuffer(make([]byte, 0, len(buf)-length+len(repl)))
	after.Write(buf[:localStart])
	after.Write(repl)
	after.Write(buf[localEnd:])

	return cleanSnippet(buf), cleanSnippet(after.Bytes()), nil
}

func cleanSnippet(buf []byte) string {
	if !utf8.Valid(buf) {
		return fmt.Sprintf("[%d preview bytes]", len(buf))
	}
	s := string(buf)
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}
