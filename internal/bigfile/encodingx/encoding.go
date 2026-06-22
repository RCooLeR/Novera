package encodingx

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Info describes a detected encoding.
type Info struct {
	Name       string
	HasBOM     bool
	Confidence float64
}

// DetectSample performs bounded encoding detection for common text encodings.
func DetectSample(sample []byte) Info {
	if len(sample) == 0 {
		return Info{Name: "UTF-8", Confidence: 0.5}
	}
	if len(sample) >= 3 && sample[0] == 0xEF && sample[1] == 0xBB && sample[2] == 0xBF {
		return Info{Name: "UTF-8", HasBOM: true, Confidence: 1.0}
	}
	if len(sample) >= 2 && sample[0] == 0xFF && sample[1] == 0xFE {
		return Info{Name: "UTF-16LE", HasBOM: true, Confidence: 1.0}
	}
	if len(sample) >= 2 && sample[0] == 0xFE && sample[1] == 0xFF {
		return Info{Name: "UTF-16BE", HasBOM: true, Confidence: 1.0}
	}
	if looksLikeUTF16LE(sample) {
		return Info{Name: "UTF-16LE", Confidence: 0.82}
	}
	if looksLikeUTF16BE(sample) {
		return Info{Name: "UTF-16BE", Confidence: 0.82}
	}
	if utf8.Valid(sample) {
		return Info{Name: "UTF-8", Confidence: 0.95}
	}
	if likelyWindows1251(sample) {
		return Info{Name: "Windows-1251", Confidence: 0.74}
	}
	return Info{Name: "Windows-1252", Confidence: 0.58}
}

// DecodeBytes decodes bytes using a supported encoding name.
func DecodeBytes(name string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	data = stripLeadingBOM(name, data)
	if strings.EqualFold(name, "UTF-8") {
		if utf8.Valid(data) {
			return string(data), nil
		}
		return "", errors.New("invalid UTF-8 input")
	}
	enc, err := codecByName(name)
	if err != nil {
		return "", err
	}
	decoded, _, err := transform.Bytes(enc.NewDecoder(), data)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// DecodeBytesBestEffort decodes supported encodings and falls back to a lossy string on failure.
func DecodeBytesBestEffort(name string, data []byte) string {
	decoded, err := DecodeBytes(name, data)
	if err == nil {
		return decoded
	}
	return string(bytes.ToValidUTF8(data, []byte("\uFFFD")))
}

// EncodeString encodes UTF-8 text into the named target encoding.
func EncodeString(name string, text string) ([]byte, error) {
	if strings.EqualFold(name, "UTF-8") {
		return []byte(text), nil
	}
	enc, err := codecByName(name)
	if err != nil {
		return nil, err
	}
	encoded, _, err := transform.Bytes(enc.NewEncoder(), []byte(text))
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// NewDecoderReader wraps a reader with a streaming decoder for the named encoding.
func NewDecoderReader(name string, r io.Reader) (io.Reader, error) {
	if strings.EqualFold(name, "UTF-8") {
		return r, nil
	}
	enc, err := codecByName(name)
	if err != nil {
		return nil, err
	}
	return transform.NewReader(r, enc.NewDecoder()), nil
}

// NewEncoderWriter wraps a writer with a streaming encoder for the named encoding.
func NewEncoderWriter(name string, w io.Writer) (io.Writer, error) {
	if strings.EqualFold(name, "UTF-8") {
		return w, nil
	}
	enc, err := codecByName(name)
	if err != nil {
		return nil, err
	}
	return transform.NewWriter(w, enc.NewEncoder()), nil
}

// BOMBytes returns the byte order mark for encodings that commonly use one.
func BOMBytes(name string) []byte {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "UTF-8":
		return []byte{0xEF, 0xBB, 0xBF}
	case "UTF-16LE":
		return []byte{0xFF, 0xFE}
	case "UTF-16BE":
		return []byte{0xFE, 0xFF}
	default:
		return nil
	}
}

func codecByName(name string) (encoding.Encoding, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "UTF-16LE":
		return xunicode.UTF16(xunicode.LittleEndian, xunicode.IgnoreBOM), nil
	case "UTF-16BE":
		return xunicode.UTF16(xunicode.BigEndian, xunicode.IgnoreBOM), nil
	case "WINDOWS-1251":
		return charmap.Windows1251, nil
	case "WINDOWS-1252":
		return charmap.Windows1252, nil
	default:
		return nil, errors.New("unsupported encoding: " + name)
	}
}

func stripLeadingBOM(name string, data []byte) []byte {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "UTF-8":
		if len(data) >= 3 && bytes.Equal(data[:3], []byte{0xEF, 0xBB, 0xBF}) {
			return data[3:]
		}
	case "UTF-16LE":
		if len(data) >= 2 && bytes.Equal(data[:2], []byte{0xFF, 0xFE}) {
			return data[2:]
		}
	case "UTF-16BE":
		if len(data) >= 2 && bytes.Equal(data[:2], []byte{0xFE, 0xFF}) {
			return data[2:]
		}
	}
	return data
}

func looksLikeUTF16LE(sample []byte) bool {
	if len(sample) < 4 {
		return false
	}
	zeros := 0
	checked := 0
	for i := 1; i < len(sample); i += 2 {
		checked++
		if sample[i] == 0 {
			zeros++
		}
	}
	return checked >= 4 && float64(zeros)/float64(checked) > 0.6
}

func looksLikeUTF16BE(sample []byte) bool {
	if len(sample) < 4 {
		return false
	}
	zeros := 0
	checked := 0
	for i := 0; i < len(sample); i += 2 {
		checked++
		if sample[i] == 0 {
			zeros++
		}
	}
	return checked >= 4 && float64(zeros)/float64(checked) > 0.6
}

func likelyWindows1251(sample []byte) bool {
	decoded, _, err := transform.Bytes(charmap.Windows1251.NewDecoder(), sample)
	if err != nil {
		return false
	}
	cyrillic := 0
	latin := 0
	for _, r := range string(decoded) {
		switch {
		case unicode.In(r, unicode.Cyrillic):
			cyrillic++
		case unicode.In(r, unicode.Latin):
			latin++
		}
	}
	return cyrillic >= 2 && cyrillic >= latin
}
