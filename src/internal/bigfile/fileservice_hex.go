package bigfile

import (
	"errors"
	"fmt"
	"strings"
)

const (
	hexWindowBytes  = 64 * 1024
	hexBytesPerLine = 16
)

var ErrHexRequestTooLarge = errors.New("hex window request exceeds the hard byte limit")

// HexLine is one 16-byte row: global offset, space-grouped hex, and ASCII.
type HexLine struct {
	Offset int64  `json:"offset"`
	Hex    string `json:"hex"`
	Ascii  string `json:"ascii"`
}

// HexWindow is a bounded, 16-byte-aligned slice rendered as hex+ASCII.
type HexWindow struct {
	FileID    string    `json:"fileId"`
	StartByte int64     `json:"startByte"`
	NextByte  int64     `json:"nextByte"`
	Lines     []HexLine `json:"lines"`
	AtBof     bool      `json:"atBof"`
	AtEof     bool      `json:"atEof"`
}

// GetHexWindow returns a bounded hex dump of the file starting at (16-byte
// aligned) startByte. Scrolling loads adjacent windows; the file is never fully
// read. Works for any file, including binary.
func (s *FileService) GetHexWindow(fileID string, startByte int64, maxBytes int) (HexWindow, error) {
	if startByte < 0 {
		return HexWindow{}, fmt.Errorf("%w: start byte must not be negative", ErrHexRequestTooLarge)
	}
	if maxBytes < 0 {
		return HexWindow{}, fmt.Errorf("%w: byte budget must not be negative", ErrHexRequestTooLarge)
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return HexWindow{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if maxBytes == 0 {
		maxBytes = hexWindowBytes
	}
	if maxBytes > maxHexWindowBytes {
		return HexWindow{}, fmt.Errorf("%w: requested %d bytes, maximum %d", ErrHexRequestTooLarge, maxBytes, maxHexWindowBytes)
	}
	size := f.Doc.Size()
	if startByte > size {
		startByte = size
	}
	startByte -= startByte % hexBytesPerLine // align to a row boundary

	// Subtract before adding so a valid sparse file near MaxInt64 cannot
	// overflow the requested end offset.
	end := size
	if int64(maxBytes) <= size-startByte {
		end = startByte + int64(maxBytes)
	}
	raw, err := f.Doc.ReadRange(startByte, end)
	if err != nil {
		return HexWindow{}, err
	}

	lines := make([]HexLine, 0, len(raw)/hexBytesPerLine+1)
	for off := 0; off < len(raw); off += hexBytesPerLine {
		n := hexBytesPerLine
		if off+n > len(raw) {
			n = len(raw) - off
		}
		var hb, ab strings.Builder
		for i := 0; i < hexBytesPerLine; i++ {
			if i == 8 {
				hb.WriteByte(' ') // gap between the two 8-byte halves
			}
			if i < n {
				b := raw[off+i]
				fmt.Fprintf(&hb, "%02x ", b)
				if b >= 0x20 && b < 0x7f {
					ab.WriteByte(b)
				} else {
					ab.WriteByte('.')
				}
			} else {
				hb.WriteString("   ")
			}
		}
		lines = append(lines, HexLine{
			Offset: startByte + int64(off),
			Hex:    strings.TrimRight(hb.String(), " "),
			Ascii:  ab.String(),
		})
	}
	return HexWindow{FileID: fileID, StartByte: startByte, NextByte: end, Lines: lines, AtBof: startByte == 0, AtEof: end >= size}, nil
}
