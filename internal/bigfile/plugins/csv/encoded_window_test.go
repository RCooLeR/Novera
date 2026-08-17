package csv

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

func TestDecodeSourceWindowSupportedEncodingsAcrossReadSeams(t *testing.T) {
	t.Parallel()
	const text = "id,note\r\n1,\u041f\u0440\u0438\u0432\u0435\u0442\r\n2,\u043c\u0438\u0440\r\n"
	tests := []struct {
		name     string
		encoding string
		bom      []byte
		encode   func(string) []byte
	}{
		{name: "UTF-8 BOM", encoding: "UTF-8", bom: utf8BOM, encode: func(value string) []byte { return []byte(value) }},
		{name: "UTF-16LE BOM", encoding: "UTF-16LE", bom: utf16LEBOM, encode: func(value string) []byte { return encodeTestUTF16(value, binary.LittleEndian) }},
		{name: "UTF-16BE BOM", encoding: "UTF-16BE", bom: utf16BEBOM, encode: func(value string) []byte { return encodeTestUTF16(value, binary.BigEndian) }},
		{name: "UTF-16LE no BOM", encoding: "UTF-16LE", encode: func(value string) []byte { return encodeTestUTF16(value, binary.LittleEndian) }},
		{name: "UTF-16BE no BOM", encoding: "UTF-16BE", encode: func(value string) []byte { return encodeTestUTF16(value, binary.BigEndian) }},
		{name: "Windows-1251", encoding: "Windows-1251", encode: encodeTestWindows1251},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw := append(append([]byte(nil), test.bom...), test.encode(text)...)
			for _, step := range []int{1, 2, 3, 7, 31} {
				decoded, err := DecodeSourceWindow(context.Background(), &encodedSeamReader{data: raw, step: step}, DecodeWindowOptions{
					Encoding: test.encoding, AtBOF: true,
					MaxRawBytes: int64(len(raw) + 1), MaxDecodedBytes: int64(3*len(raw) + 16), TrackOffsets: true,
				})
				if err != nil {
					t.Fatalf("step %d: DecodeSourceWindow() error = %v", step, err)
				}
				if string(decoded.Data) != text {
					t.Fatalf("step %d: decoded = %q, want %q", step, decoded.Data, text)
				}
				if decoded.Truncated {
					t.Fatalf("step %d: complete source reported truncated", step)
				}
				if len(decoded.RuneDecodedEnds) == 0 || len(decoded.RuneDecodedEnds) != len(decoded.RuneRawEnds) {
					t.Fatalf("step %d: invalid checkpoint arrays: %d/%d", step, len(decoded.RuneDecodedEnds), len(decoded.RuneRawEnds))
				}
				rawEnd, err := decoded.RawOffsetForDecodedEnd(len(decoded.Data))
				if err != nil || rawEnd != int64(len(raw)) {
					t.Fatalf("step %d: final raw offset = %d, %v; want %d", step, rawEnd, err, len(raw))
				}
			}
		})
	}
}

func TestDecodeSourceWindowTrimsOnlyIncompleteUTF8AtBoundedSeam(t *testing.T) {
	t.Parallel()
	raw := []byte("a,\U0001f600\nnext,row\n")
	// a, consumes two bytes; retain only the first two bytes of the four-byte
	// emoji and use the following raw byte solely as the truncation probe.
	decoded, err := DecodeSourceWindow(context.Background(), bytes.NewReader(raw), DecodeWindowOptions{
		Encoding: "UTF-8", AtBOF: true, MaxRawBytes: 4, MaxDecodedBytes: 16, TrackOffsets: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(decoded.Data); got != "a," {
		t.Fatalf("decoded seam = %q, want %q", got, "a,")
	}
	if !decoded.Truncated || decoded.RawBytesDecoded != 2 {
		t.Fatalf("seam state = %+v, want truncated with two decoded raw bytes", decoded)
	}

	bad := []byte{'a', 0xFF, 'b'}
	if _, err := DecodeSourceWindow(context.Background(), bytes.NewReader(bad), DecodeWindowOptions{
		Encoding: "UTF-8", AtBOF: true, MaxRawBytes: 8, MaxDecodedBytes: 16,
	}); !errors.Is(err, ErrCSVSourceEncodingMalformed) {
		t.Fatalf("invalid UTF-8 error = %v, want ErrCSVSourceEncodingMalformed", err)
	}
	if _, err := DecodeSourceWindow(context.Background(), bytes.NewReader(bad), DecodeWindowOptions{
		Encoding: "UTF-8", AtBOF: true, MaxRawBytes: 2, MaxDecodedBytes: 16,
	}); !errors.Is(err, ErrCSVSourceEncodingMalformed) {
		t.Fatalf("invalid UTF-8 at bounded seam error = %v, want ErrCSVSourceEncodingMalformed", err)
	}
}

func TestDecodeSourceWindowUTF16SurrogateSeamAndMalformedPairs(t *testing.T) {
	t.Parallel()
	raw := append(append([]byte(nil), utf16LEBOM...), encodeTestUTF16("a,\U0001f600\n", binary.LittleEndian)...)
	// BOM (2), a/comma (4), and the high surrogate (2) fit. The low surrogate
	// begins at the truncation probe and must not become U+FFFD.
	decoded, err := DecodeSourceWindow(context.Background(), bytes.NewReader(raw), DecodeWindowOptions{
		Encoding: "UTF-16LE", AtBOF: true, MaxRawBytes: 8, MaxDecodedBytes: 32, TrackOffsets: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(decoded.Data); got != "a," {
		t.Fatalf("decoded surrogate seam = %q, want %q", got, "a,")
	}
	if decoded.RawBytesDecoded != 6 || !decoded.Truncated {
		t.Fatalf("surrogate seam state = %+v, want six raw bytes decoded and truncated", decoded)
	}

	malformed := append(append([]byte(nil), utf16LEBOM...), 0x00, 0xD8, 'x', 0x00)
	if _, err := DecodeSourceWindow(context.Background(), bytes.NewReader(malformed), DecodeWindowOptions{
		Encoding: "UTF-16LE", AtBOF: true, MaxRawBytes: 32, MaxDecodedBytes: 32,
	}); !errors.Is(err, ErrCSVSourceEncodingMalformed) {
		t.Fatalf("malformed surrogate error = %v, want ErrCSVSourceEncodingMalformed", err)
	}
}

func TestParseRecordWindowUsesLogicalMultilineBoundaries(t *testing.T) {
	t.Parallel()
	const header = "id,note\r\n"
	partial := []byte(header + "1,\"line one\r\nline")
	window, err := ParseRecordWindow(context.Background(), partial, RecordWindowOptions{
		Delimiter: ',', SourceTruncated: true, MaxRecordBytes: 1024,
		MaxRows: 10, MaxColumns: 10, MaxCells: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Rows) != 1 || !equalRecord(window.Rows[0], []string{"id", "note"}) {
		t.Fatalf("rows = %#v, want only complete header", window.Rows)
	}
	if window.DecodedEnd != len(header) {
		t.Fatalf("decoded continuation = %d, want %d", window.DecodedEnd, len(header))
	}

	remaining := []byte("1,\"line one\r\nline two\"\r\n2,tail\r\n")
	next, err := ParseRecordWindow(context.Background(), remaining, RecordWindowOptions{
		Delimiter: ',', MaxRecordBytes: 1024,
		MaxRows: 10, MaxColumns: 10, MaxCells: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Rows) != 2 || next.Rows[0][1] != "line one\nline two" || next.Rows[1][1] != "tail" {
		t.Fatalf("multiline rows = %#v", next.Rows)
	}
}

func TestParseRecordWindowFailsClosedForOversizedOrMalformedFirstRecord(t *testing.T) {
	t.Parallel()
	_, err := ParseRecordWindow(context.Background(), []byte("1,\"unfinished\ninside"), RecordWindowOptions{
		Delimiter: ',', SourceTruncated: true, MaxRecordBytes: 1024,
		MaxRows: 10, MaxColumns: 10, MaxCells: 100,
	})
	if !errors.Is(err, ErrCSVGridRecordExceedsWindow) {
		t.Fatalf("partial first-record error = %v, want ErrCSVGridRecordExceedsWindow", err)
	}

	_, err = ParseRecordWindow(context.Background(), []byte("1,\"bad\"suffix\n"), RecordWindowOptions{
		Delimiter: ',', MaxRecordBytes: 1024,
		MaxRows: 10, MaxColumns: 10, MaxCells: 100,
	})
	if err == nil {
		t.Fatal("malformed complete CSV unexpectedly parsed")
	}

	_, err = ParseRecordWindow(context.Background(), []byte("a,b,c\n"), RecordWindowOptions{
		Delimiter: ',', MaxRecordBytes: 1024,
		MaxRows: 10, MaxColumns: 2, MaxCells: 100,
	})
	if !errors.Is(err, ErrCSVGridShapeLimit) {
		t.Fatalf("wide-row error = %v, want ErrCSVGridShapeLimit", err)
	}
}

func TestCompleteRecordPrefixDoesNotTreatQuotedNewlineAsBoundary(t *testing.T) {
	t.Parallel()
	data := []byte("id,note\n1,\"one\ntwo\"\n2,\"partial\n")
	prefix, omitted, err := CompleteRecordPrefix(context.Background(), data, ',', 1024, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "id,note\n1,\"one\ntwo\"\n"
	if string(prefix) != want || !omitted {
		t.Fatalf("prefix = %q, omitted=%t; want %q, true", prefix, omitted, want)
	}
}

func encodeTestUTF16(value string, order binary.ByteOrder) []byte {
	units := utf16.Encode([]rune(value))
	out := make([]byte, len(units)*2)
	for i, unit := range units {
		order.PutUint16(out[i*2:], unit)
	}
	return out
}

func encodeTestWindows1251(value string) []byte {
	encoded, _, err := transform.Bytes(charmap.Windows1251.NewEncoder(), []byte(value))
	if err != nil {
		panic(err)
	}
	return encoded
}

type encodedSeamReader struct {
	data []byte
	step int
}

func (r *encodedSeamReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.step, len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func equalRecord(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
