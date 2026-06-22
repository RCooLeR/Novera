package csv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
)

var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}
)

// ErrUTF16Input is returned when input looks like UTF-16. The CSV tools operate
// on UTF-8 and don't transcode, so this gives the user an actionable message
// instead of a confusing "control character U+0000" failure on the NUL bytes.
var ErrUTF16Input = errors.New("input looks like UTF-16; re-save the file as UTF-8")

// stripUTF8BOM removes a leading UTF-8 BOM (EF BB BF) if present. Windows tools
// (Excel, PowerShell Export-Csv) emit one by default, and Go's encoding/csv does
// not strip it — left in place it corrupts the first column name / first value.
func stripUTF8BOM(b []byte) []byte { return bytes.TrimPrefix(b, utf8BOM) }

// detectUTF16BOM reports whether b begins with a UTF-16 byte-order mark.
func detectUTF16BOM(b []byte) bool {
	return bytes.HasPrefix(b, utf16LEBOM) || bytes.HasPrefix(b, utf16BEBOM)
}

// skipInputBOM peeks a streaming reader: it errors on a UTF-16 BOM and discards
// a UTF-8 BOM so it isn't counted or fed to the CSV parser.
func skipInputBOM(br *bufio.Reader) error {
	peek, _ := br.Peek(3)
	if detectUTF16BOM(peek) {
		return ErrUTF16Input
	}
	if len(peek) >= len(utf8BOM) && bytes.Equal(peek[:len(utf8BOM)], utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	return nil
}

func readBoundedSample(ctx context.Context, r io.Reader, maxBytes int64) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if detectUTF16BOM(data) {
		return nil, ErrUTF16Input
	}
	return stripUTF8BOM(data), nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
