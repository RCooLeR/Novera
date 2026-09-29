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

var ErrEncodingConfirmationRequired = errors.New("source encoding is ambiguous; choose the source encoding explicitly before transforming")

// Info describes a detected encoding.
type Info struct {
	Name                 string
	HasBOM               bool
	Confidence           float64
	RequiresConfirmation bool
}

// DetectSample performs bounded encoding detection for common text encodings.
func DetectSample(sample []byte) Info {
	return detectSample(sample, false)
}

// DetectPrefixSample accepts a valid UTF-8 prefix whose final bytes may be an
// incomplete rune. Callers must use it only when more source bytes exist.
func DetectPrefixSample(sample []byte) Info {
	return detectSample(sample, true)
}

func detectSample(sample []byte, allowIncompleteUTF8Tail bool) Info {
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
	if ordinaryUTF8Sample(sample, allowIncompleteUTF8Tail) {
		return Info{Name: "UTF-8", Confidence: 0.95}
	}
	if utf16Info, detected := detectBOMlessUTF16(sample, allowIncompleteUTF8Tail); detected {
		return utf16Info
	}
	if utf8.Valid(sample) || (allowIncompleteUTF8Tail && ValidUTF8Prefix(sample)) {
		return Info{Name: "UTF-8", Confidence: 0.95}
	}
	if likelyWindows1251(sample) {
		return Info{Name: "Windows-1251", Confidence: 0.74}
	}
	return Info{Name: "Windows-1252", Confidence: 0.58}
}

func ordinaryUTF8Sample(sample []byte, allowIncompleteTail bool) bool {
	if !utf8.Valid(sample) && !(allowIncompleteTail && ValidUTF8Prefix(sample)) {
		return false
	}
	for _, value := range sample {
		if value == 0 || value < 0x09 || (value > 0x0D && value < 0x20) {
			return false
		}
	}
	return true
}

// ValidUTF8Prefix reports whether sample is valid UTF-8 or differs only by a
// truncated final rune prefix. Invalid leaders, continuations, overlong forms,
// surrogate encodings, and out-of-range code points remain rejected.
func ValidUTF8Prefix(sample []byte) bool {
	if utf8.Valid(sample) {
		return true
	}
	firstCandidate := len(sample) - 3
	if firstCandidate < 0 {
		firstCandidate = 0
	}
	for start := len(sample) - 1; start >= firstCandidate; start-- {
		width := utf8SequenceWidth(sample[start])
		if width == 0 {
			continue
		}
		tail := sample[start:]
		if len(tail) >= width || !utf8.Valid(sample[:start]) {
			continue
		}
		if validIncompleteUTF8Sequence(tail, width) {
			return true
		}
	}
	return false
}

func utf8SequenceWidth(lead byte) int {
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		return 2
	case lead >= 0xE0 && lead <= 0xEF:
		return 3
	case lead >= 0xF0 && lead <= 0xF4:
		return 4
	default:
		return 0
	}
}

func validIncompleteUTF8Sequence(tail []byte, width int) bool {
	if len(tail) == 0 || utf8SequenceWidth(tail[0]) != width || len(tail) >= width {
		return false
	}
	for i := 1; i < len(tail); i++ {
		continuation := tail[i]
		if continuation < 0x80 || continuation > 0xBF {
			return false
		}
		if i != 1 {
			continue
		}
		switch tail[0] {
		case 0xE0:
			if continuation < 0xA0 {
				return false
			}
		case 0xED:
			if continuation > 0x9F {
				return false
			}
		case 0xF0:
			if continuation < 0x90 {
				return false
			}
		case 0xF4:
			if continuation > 0x8F {
				return false
			}
		}
	}
	return true
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
