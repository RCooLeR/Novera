package workspace

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Encoding labels reported in FileContent.Encoding and accepted by WriteFile.
// They are stable wire values, so the frontend can match on them directly.
const (
	encUTF8    = "utf-8"
	encUTF8BOM = "utf-8-bom"
	encUTF16LE = "utf-16le"
	encUTF16BE = "utf-16be"
	encLatin1  = "latin-1"
)

var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// decodeText classifies data and, when it is decodable text, returns the UTF-8
// text plus a canonical encoding label. ok=false means the bytes are genuinely
// binary and should be shown as a hex dump. This is the honesty fix for the
// common Windows case: UTF-16 and legacy single-byte files previously tripped
// the NUL/invalid-UTF-8 binary check and rendered as raw hex with no
// explanation; now they open as readable text and the UI can warn about the
// encoding (and that saving may convert it).
func decodeText(data []byte) (text, enc string, ok bool) {
	// 1) A BOM is unambiguous — trust it first.
	switch {
	case bytes.HasPrefix(data, bomUTF8):
		body := data[len(bomUTF8):]
		if utf8.Valid(body) {
			return string(body), encUTF8BOM, true
		}
		return "", "", false
	case bytes.HasPrefix(data, bomUTF16LE):
		if s, ok2 := decodeUTF16(data[len(bomUTF16LE):], false); ok2 {
			return s, encUTF16LE, true
		}
		return "", "", false
	case bytes.HasPrefix(data, bomUTF16BE):
		if s, ok2 := decodeUTF16(data[len(bomUTF16BE):], true); ok2 {
			return s, encUTF16BE, true
		}
		return "", "", false
	}
	// 2) A NUL in the head means this isn't plain UTF-8/Latin-1 text (NUL is valid
	//    UTF-8, so we must check it before utf8.Valid): it's either BOM-less UTF-16
	//    (a NUL in every other byte) or genuinely binary.
	head := data
	if len(head) > binarySniffBytes {
		head = head[:binarySniffBytes]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		if bigEndian, isUTF16 := sniffUTF16(data); isUTF16 {
			if s, ok2 := decodeUTF16(data, bigEndian); ok2 {
				if bigEndian {
					return s, encUTF16BE, true
				}
				return s, encUTF16LE, true
			}
		}
		return "", "", false // binary
	}
	// 3) No NUL, valid UTF-8 — the common case.
	if utf8.Valid(data) {
		return string(data), encUTF8, true
	}
	// 4) No NUL but invalid UTF-8 -> a legacy single-byte encoding. Latin-1 maps
	//    every byte to a code point losslessly, so it is always viewable (and
	//    labelled, so the UI can warn it is not UTF-8).
	var b strings.Builder
	b.Grow(len(data))
	for _, c := range data {
		b.WriteRune(rune(c))
	}
	return b.String(), encLatin1, true
}

// sniffUTF16 reports whether data looks like BOM-less UTF-16 and, if so, its
// byte order (true = big-endian). Heuristic: ASCII-ish text encoded as UTF-16
// puts a 0x00 in the high byte of (almost) every 16-bit unit. A strong majority
// of zero high-bytes on one side indicates that byte order; the threshold is
// deliberately high so real binary isn't misread as text.
func sniffUTF16(data []byte) (bigEndian, ok bool) {
	n := len(data)
	if n > binarySniffBytes {
		n = binarySniffBytes
	}
	if n < 2 {
		return false, false
	}
	n &^= 1 // whole 16-bit units only
	zerosEven, zerosOdd, pairs := 0, 0, 0
	for i := 0; i+1 < n; i += 2 {
		if data[i] == 0 {
			zerosEven++
		}
		if data[i+1] == 0 {
			zerosOdd++
		}
		pairs++
	}
	if pairs == 0 {
		return false, false
	}
	if zerosOdd > zerosEven && zerosOdd*10 >= pairs*8 {
		return false, true // high byte is the odd one -> little-endian
	}
	if zerosEven > zerosOdd && zerosEven*10 >= pairs*8 {
		return true, true // big-endian
	}
	return false, false
}

func decodeUTF16(b []byte, bigEndian bool) (string, bool) {
	if len(b)%2 != 0 {
		return "", false // not whole 16-bit units
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if bigEndian {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(u)), true
}

// encodeText converts UTF-8 editor content into the on-disk bytes for enc,
// round-tripping a non-UTF-8 file on save. An empty or utf-8 enc writes plain
// UTF-8. latin-1 fails loudly if the content gained a character it cannot
// represent, rather than silently corrupting it.
func encodeText(content, enc string) ([]byte, error) {
	switch enc {
	case "", encUTF8:
		return []byte(content), nil
	case encUTF8BOM:
		return append(append([]byte{}, bomUTF8...), content...), nil
	case encUTF16LE, encUTF16BE:
		be := enc == encUTF16BE
		units := utf16.Encode([]rune(content))
		out := make([]byte, 0, 2+len(units)*2)
		if be {
			out = append(out, bomUTF16BE...)
		} else {
			out = append(out, bomUTF16LE...)
		}
		for _, u := range units {
			if be {
				out = append(out, byte(u>>8), byte(u))
			} else {
				out = append(out, byte(u), byte(u>>8))
			}
		}
		return out, nil
	case encLatin1:
		out := make([]byte, 0, len(content))
		for _, r := range content {
			if r > 0xFF {
				return nil, fmt.Errorf("cannot save as latin-1: text contains %q which is outside the latin-1 range — choose UTF-8 instead", r)
			}
			out = append(out, byte(r))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported encoding %q", enc)
	}
}
