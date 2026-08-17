package csv

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"novera/internal/bigfile/encodingx"
)

const encodedWindowReadChunkBytes = 32 * 1024

var (
	// ErrCSVSourceEncodingUnsupported identifies a source encoding that the CSV
	// decoder cannot map to UTF-8 without guessing.
	ErrCSVSourceEncodingUnsupported = errors.New("unsupported CSV source encoding")
	// ErrCSVSourceEncodingMalformed identifies malformed bytes in an otherwise
	// supported source encoding. The decoder never substitutes U+FFFD.
	ErrCSVSourceEncodingMalformed = errors.New("malformed CSV source encoding")
	// ErrCSVGridRecordExceedsWindow reports that the first logical record at a
	// cursor cannot fit inside the caller's bounded raw-byte page.
	ErrCSVGridRecordExceedsWindow = errors.New("CSV logical record exceeds the grid window")
	// ErrCSVGridShapeLimit reports a row/column/cell shape that would exceed the
	// bounded backend response and frontend DOM contract.
	ErrCSVGridShapeLimit = errors.New("CSV grid shape exceeds the display limit")
)

// DecodeWindowOptions configures a bounded raw-source to UTF-8 conversion.
// MaxRawBytes excludes the one-byte truncation probe but includes a leading
// BOM. MaxDecodedBytes is an independent hard ceiling for the UTF-8 result.
type DecodeWindowOptions struct {
	Encoding        string
	AtBOF           bool
	MaxRawBytes     int64
	MaxDecodedBytes int64
	TrackOffsets    bool
}

// DecodedWindow retains an exact mapping only when TrackOffsets is requested.
// RuneDecodedEnds and RuneRawEnds have one entry per decoded rune. Raw values
// are relative to the beginning of the supplied reader and include a stripped
// BOM. PrefixRawBytes is the raw position represented by decoded offset zero.
type DecodedWindow struct {
	Data            []byte
	RawBytesRead    int64
	RawBytesDecoded int64
	PrefixRawBytes  int64
	Truncated       bool
	RuneDecodedEnds []int
	RuneRawEnds     []int64
}

// DecodeSourceWindow reads at most MaxRawBytes plus one truncation-probe byte,
// strictly decodes a supported source encoding, and optionally records exact
// decoded-rune/raw-byte checkpoints. It never reads the entire source unless
// the source itself fits within the requested raw budget.
func DecodeSourceWindow(ctx context.Context, source io.Reader, opts DecodeWindowOptions) (DecodedWindow, error) {
	if source == nil {
		return DecodedWindow{}, errors.New("CSV source reader is required")
	}
	if opts.MaxRawBytes <= 0 || opts.MaxRawBytes > MaxSampleBytes {
		return DecodedWindow{}, fmt.Errorf("CSV raw window limit %d is outside 1..%d", opts.MaxRawBytes, MaxSampleBytes)
	}
	if opts.MaxDecodedBytes <= 0 || opts.MaxDecodedBytes > 3*MaxSampleBytes {
		return DecodedWindow{}, fmt.Errorf("CSV decoded window limit %d is outside 1..%d", opts.MaxDecodedBytes, 3*MaxSampleBytes)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DecodedWindow{}, err
	}

	raw, truncated, err := readRawWindow(ctx, source, opts.MaxRawBytes)
	if err != nil {
		return DecodedWindow{}, err
	}
	result := DecodedWindow{RawBytesRead: int64(len(raw)), Truncated: truncated}
	if truncated {
		raw = raw[:len(raw)-1]
		result.RawBytesRead--
	}

	encodingName, err := normalizeSourceEncoding(opts.Encoding)
	if err != nil {
		return DecodedWindow{}, err
	}
	switch encodingName {
	case "UTF-8":
		err = decodeUTF8Window(raw, opts, &result)
	case "UTF-16LE":
		err = decodeUTF16Window(raw, binary.LittleEndian, opts, &result)
	case "UTF-16BE":
		err = decodeUTF16Window(raw, binary.BigEndian, opts, &result)
	case "WINDOWS-1251":
		err = decodeWindows1251Window(raw, opts, &result)
	default:
		err = fmt.Errorf("%w: %q", ErrCSVSourceEncodingUnsupported, opts.Encoding)
	}
	if err != nil {
		return DecodedWindow{}, err
	}
	result.Data = result.Data[:len(result.Data):len(result.Data)]
	result.RuneDecodedEnds = result.RuneDecodedEnds[:len(result.RuneDecodedEnds):len(result.RuneDecodedEnds)]
	result.RuneRawEnds = result.RuneRawEnds[:len(result.RuneRawEnds):len(result.RuneRawEnds)]
	return result, nil
}

func readRawWindow(ctx context.Context, source io.Reader, maxRawBytes int64) ([]byte, bool, error) {
	// maxRawBytes is capped at 64 MiB, so adding the one-byte probe is safe.
	target := maxRawBytes + 1
	initial := minInt64(target, 512)
	raw := make([]byte, 0, int(initial))
	scratch := make([]byte, int(minInt64(target, encodedWindowReadChunkBytes)))
	emptyReads := 0
	for int64(len(raw)) < target {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		want := minInt64(target-int64(len(raw)), int64(len(scratch)))
		n, readErr := source.Read(scratch[:int(want)])
		if n < 0 || n > int(want) {
			return nil, false, fmt.Errorf("CSV source returned invalid byte count %d for %d-byte request", n, want)
		}
		if n > 0 {
			raw = appendWithBoundedCapacity(raw, scratch[:n], int(target))
			emptyReads = 0
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= maxConsecutiveEmptyReads {
				return nil, false, io.ErrNoProgress
			}
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, false, readErr
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return raw, int64(len(raw)) > maxRawBytes, nil
}

func normalizeSourceEncoding(name string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "", "UTF-8", "UTF8", "ASCII", "US-ASCII":
		return "UTF-8", nil
	case "UTF-16LE", "UTF-16BE", "WINDOWS-1251":
		return strings.ToUpper(strings.TrimSpace(name)), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrCSVSourceEncodingUnsupported, name)
	}
}

func decodeUTF8Window(raw []byte, opts DecodeWindowOptions, result *DecodedWindow) error {
	prefix := 0
	if opts.AtBOF && bytes.HasPrefix(raw, utf8BOM) {
		prefix = len(utf8BOM)
	} else if opts.AtBOF && detectUTF16BOM(raw) {
		return fmt.Errorf("%w: UTF-16 BOM contradicts UTF-8 metadata", ErrCSVSourceEncodingMalformed)
	}
	payload := raw[prefix:]
	validEnd, err := validUTF8WindowEnd(payload, result.Truncated)
	if err != nil {
		return err
	}
	if validEnd < len(payload) {
		result.Truncated = true
		payload = payload[:validEnd]
	}
	if int64(len(payload)) > opts.MaxDecodedBytes {
		return fmt.Errorf("decoded UTF-8 window needs %d bytes, limit is %d", len(payload), opts.MaxDecodedBytes)
	}
	result.PrefixRawBytes = int64(prefix)
	result.RawBytesDecoded = int64(prefix + len(payload))
	result.Data = append(result.Data, payload...)
	if !opts.TrackOffsets {
		return nil
	}
	for decodedStart := 0; decodedStart < len(payload); {
		_, width := utf8.DecodeRune(payload[decodedStart:])
		decodedStart += width
		result.RuneDecodedEnds = append(result.RuneDecodedEnds, decodedStart)
		result.RuneRawEnds = append(result.RuneRawEnds, int64(prefix+decodedStart))
	}
	return nil
}

func validUTF8WindowEnd(payload []byte, truncated bool) (int, error) {
	if utf8.Valid(payload) {
		return len(payload), nil
	}
	if !truncated || !encodingx.ValidUTF8Prefix(payload) {
		return 0, fmt.Errorf("%w: invalid UTF-8 input", ErrCSVSourceEncodingMalformed)
	}
	minimum := len(payload) - 3
	if minimum < 0 {
		minimum = 0
	}
	for end := len(payload) - 1; end >= minimum; end-- {
		if utf8.Valid(payload[:end]) {
			return end, nil
		}
	}
	return 0, fmt.Errorf("%w: invalid UTF-8 before the bounded sample seam", ErrCSVSourceEncodingMalformed)
}

func decodeUTF16Window(raw []byte, order binary.ByteOrder, opts DecodeWindowOptions, result *DecodedWindow) error {
	prefix := 0
	if opts.AtBOF && bytes.HasPrefix(raw, utf8BOM) {
		return fmt.Errorf("%w: UTF-8 BOM contradicts %s metadata", ErrCSVSourceEncodingMalformed, opts.Encoding)
	}
	if opts.AtBOF && len(raw) >= 2 {
		matching := order == binary.LittleEndian && bytes.HasPrefix(raw, utf16LEBOM) || order == binary.BigEndian && bytes.HasPrefix(raw, utf16BEBOM)
		opposite := order == binary.LittleEndian && bytes.HasPrefix(raw, utf16BEBOM) || order == binary.BigEndian && bytes.HasPrefix(raw, utf16LEBOM)
		if opposite {
			return fmt.Errorf("%w: UTF-16 BOM contradicts %s metadata", ErrCSVSourceEncodingMalformed, opts.Encoding)
		}
		if matching {
			prefix = 2
		}
	}
	payload := raw[prefix:]
	if len(payload)%2 != 0 {
		if !result.Truncated {
			return fmt.Errorf("%w: odd-length UTF-16 input", ErrCSVSourceEncodingMalformed)
		}
		payload = payload[:len(payload)-1]
		result.Truncated = true
	}
	result.PrefixRawBytes = int64(prefix)
	for i := 0; i < len(payload); {
		unit := order.Uint16(payload[i : i+2])
		r := rune(unit)
		rawWidth := 2
		switch {
		case unit >= 0xD800 && unit <= 0xDBFF:
			if i+4 > len(payload) {
				if result.Truncated {
					payload = payload[:i]
					result.Truncated = true
					break
				}
				return fmt.Errorf("%w: truncated UTF-16 surrogate pair", ErrCSVSourceEncodingMalformed)
			}
			next := order.Uint16(payload[i+2 : i+4])
			if next < 0xDC00 || next > 0xDFFF {
				return fmt.Errorf("%w: invalid UTF-16 surrogate pair", ErrCSVSourceEncodingMalformed)
			}
			r = utf16.DecodeRune(r, rune(next))
			rawWidth = 4
		case unit >= 0xDC00 && unit <= 0xDFFF:
			return fmt.Errorf("%w: unpaired UTF-16 low surrogate", ErrCSVSourceEncodingMalformed)
		}
		if i >= len(payload) {
			break
		}
		var encoded [utf8.UTFMax]byte
		n := utf8.EncodeRune(encoded[:], r)
		if int64(len(result.Data)+n) > opts.MaxDecodedBytes {
			return fmt.Errorf("decoded UTF-16 window exceeds the %d-byte UTF-8 limit", opts.MaxDecodedBytes)
		}
		result.Data = append(result.Data, encoded[:n]...)
		i += rawWidth
		if opts.TrackOffsets {
			result.RuneDecodedEnds = append(result.RuneDecodedEnds, len(result.Data))
			result.RuneRawEnds = append(result.RuneRawEnds, int64(prefix+i))
		}
	}
	result.RawBytesDecoded = int64(prefix + len(payload))
	return nil
}

func decodeWindows1251Window(raw []byte, opts DecodeWindowOptions, result *DecodedWindow) error {
	if opts.AtBOF && (bytes.HasPrefix(raw, utf8BOM) || detectUTF16BOM(raw)) {
		return fmt.Errorf("%w: BOM contradicts Windows-1251 metadata", ErrCSVSourceEncodingMalformed)
	}
	for i, value := range raw {
		r := charmap.Windows1251.DecodeByte(value)
		if r == utf8.RuneError {
			return fmt.Errorf("%w: undefined Windows-1251 byte 0x%02X at raw byte %d", ErrCSVSourceEncodingMalformed, value, i)
		}
		var encoded [utf8.UTFMax]byte
		n := utf8.EncodeRune(encoded[:], r)
		if int64(len(result.Data)+n) > opts.MaxDecodedBytes {
			return fmt.Errorf("decoded Windows-1251 window exceeds the %d-byte UTF-8 limit", opts.MaxDecodedBytes)
		}
		result.Data = append(result.Data, encoded[:n]...)
		if opts.TrackOffsets {
			result.RuneDecodedEnds = append(result.RuneDecodedEnds, len(result.Data))
			result.RuneRawEnds = append(result.RuneRawEnds, int64(i+1))
		}
	}
	result.RawBytesDecoded = int64(len(raw))
	return nil
}

// RawOffsetForDecodedEnd maps an exact decoded rune boundary back to its raw
// byte boundary. It fails closed if a parser supplies a non-rune boundary.
func (w DecodedWindow) RawOffsetForDecodedEnd(decodedEnd int) (int64, error) {
	if decodedEnd == 0 {
		return w.PrefixRawBytes, nil
	}
	index, found := searchExactInt(w.RuneDecodedEnds, decodedEnd)
	if !found || index >= len(w.RuneRawEnds) {
		return 0, fmt.Errorf("decoded CSV offset %d is not an exact source-rune boundary", decodedEnd)
	}
	return w.RuneRawEnds[index], nil
}

func searchExactInt(values []int, target int) (int, bool) {
	low, high := 0, len(values)
	for low < high {
		mid := int(uint(low+high) >> 1)
		if values[mid] < target {
			low = mid + 1
		} else {
			high = mid
		}
	}
	return low, low < len(values) && values[low] == target
}

type RecordWindowOptions struct {
	Delimiter       rune
	SourceTruncated bool
	MaxRecordBytes  int64
	MaxRows         int
	MaxColumns      int
	MaxCells        int
}

type RecordWindow struct {
	Rows       [][]string
	Columns    int
	Cells      int
	DecodedEnd int
	AtEOF      bool
}

// ParseRecordWindow parses only complete logical CSV records. When the raw
// source was truncated at the window seam, a trailing partial record is never
// returned. The continuation is the parser-confirmed decoded record boundary.
func ParseRecordWindow(ctx context.Context, data []byte, opts RecordWindowOptions) (RecordWindow, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ValidateDelimiter(opts.Delimiter); err != nil {
		return RecordWindow{}, err
	}
	if opts.MaxRows <= 0 || opts.MaxRows > MaxSampleRows {
		return RecordWindow{}, fmt.Errorf("CSV grid row limit %d is outside 1..%d", opts.MaxRows, MaxSampleRows)
	}
	if opts.MaxColumns <= 0 || opts.MaxCells <= 0 {
		return RecordWindow{}, errors.New("CSV grid column and cell limits must be positive")
	}
	reader, err := newBoundedCSVReader(ctx, bytes.NewReader(data), csvReaderConfig{
		Delimiter: opts.Delimiter, MaxRecordBytes: opts.MaxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return RecordWindow{}, err
	}

	result := RecordWindow{Rows: make([][]string, 0, min(opts.MaxRows, 256))}
	for len(result.Rows) < opts.MaxRows {
		if err := contextErr(ctx); err != nil {
			return RecordWindow{}, err
		}
		record, readErr := reader.Read()
		inputEnd := int(reader.InputOffset())
		if errors.Is(readErr, io.EOF) {
			result.AtEOF = !opts.SourceTruncated
			break
		}
		if readErr != nil {
			if opts.SourceTruncated && inputEnd >= len(data) {
				break
			}
			return RecordWindow{}, readErr
		}
		completeAtSeam := inputEnd > 0 && data[inputEnd-1] == '\n'
		if opts.SourceTruncated && inputEnd >= len(data) && !completeAtSeam {
			break
		}
		if len(record) > opts.MaxColumns {
			return RecordWindow{}, fmt.Errorf("%w: record has %d columns, limit is %d", ErrCSVGridShapeLimit, len(record), opts.MaxColumns)
		}
		if result.Cells > opts.MaxCells-len(record) {
			return RecordWindow{}, fmt.Errorf("%w: response would exceed %d cells", ErrCSVGridShapeLimit, opts.MaxCells)
		}
		result.Rows = append(result.Rows, append([]string(nil), record...))
		result.Cells += len(record)
		if len(record) > result.Columns {
			result.Columns = len(record)
		}
		result.DecodedEnd = inputEnd
	}
	if len(result.Rows) == 0 && opts.SourceTruncated && len(data) > 0 {
		return RecordWindow{}, fmt.Errorf("%w: no complete record fits in the bounded raw page", ErrCSVGridRecordExceedsWindow)
	}
	if result.DecodedEnd == len(data) && !opts.SourceTruncated {
		result.AtEOF = true
	}
	return result, nil
}
