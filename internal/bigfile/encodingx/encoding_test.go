package encodingx

import (
	"bytes"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

func TestDetectSampleUTF8BOM(t *testing.T) {
	info := DetectSample([]byte{0xEF, 0xBB, 0xBF, 'h', 'i'})
	if info.Name != "UTF-8" || !info.HasBOM {
		t.Fatalf("info = %+v", info)
	}
}

func TestDetectSampleUTF16NoBOM(t *testing.T) {
	info := DetectSample([]byte{'H', 0x00, 'i', 0x00, '\n', 0x00, 'x', 0x00})
	if info.Name != "UTF-16LE" {
		t.Fatalf("info = %+v, want UTF-16LE", info)
	}
}

func TestDetectPrefixSampleAcceptsOnlyValidIncompleteUTF8Tails(t *testing.T) {
	for _, value := range []string{"¢", "€", "😀"} {
		encoded := []byte(value)
		for kept := 1; kept < len(encoded); kept++ {
			sample := append([]byte("valid prefix "), encoded[:kept]...)
			if !ValidUTF8Prefix(sample) {
				t.Fatalf("ValidUTF8Prefix rejected %x (%d/%d bytes)", encoded, kept, len(encoded))
			}
			if info := DetectPrefixSample(sample); info.Name != "UTF-8" {
				t.Fatalf("DetectPrefixSample(%x) = %+v, want UTF-8", sample, info)
			}
			if info := DetectSample(sample); info.Name == "UTF-8" {
				t.Fatalf("complete-sample detector accepted truncated input %x", sample)
			}
		}
	}
}

func TestValidUTF8PrefixRejectsInvalidTailForms(t *testing.T) {
	tests := [][]byte{
		{'o', 'k', 0x80},
		{'o', 'k', 0xC0},
		{'o', 'k', 0xE0, 0x80},
		{'o', 'k', 0xED, 0xA0},
		{'o', 'k', 0xF0, 0x80},
		{'o', 'k', 0xF4, 0x90},
		{'o', 'k', 0xF5},
		{'o', 'k', 0xE2, 0x28},
	}
	for _, sample := range tests {
		if ValidUTF8Prefix(sample) {
			t.Fatalf("invalid UTF-8 tail accepted: %x", sample)
		}
		if info := DetectPrefixSample(sample); info.Name == "UTF-8" {
			t.Fatalf("invalid prefix detected as UTF-8: %x", sample)
		}
	}
}

func TestDetectBOMlessUTF16AmbiguityRequiresConfirmation(t *testing.T) {
	data := []byte{
		0x4E, 0x9F, 0x4F, 0x9E, 0x50, 0x9D, 0x51, 0x9C,
		0x52, 0x9B, 0x53, 0x9A, 0x54, 0x99, 0x55, 0x98,
	}
	info := DetectSample(data)
	if !info.RequiresConfirmation || info.Name != ambiguousUTF16Name {
		t.Fatalf("ambiguous UTF-16 = %+v, want explicit confirmation", info)
	}
}

func TestDetectSampleWindows1251(t *testing.T) {
	info := DetectSample([]byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2}) // Привет
	if info.Name != "Windows-1251" {
		t.Fatalf("info = %+v, want Windows-1251", info)
	}
}

func TestDecodeAndEncodeUTF16LE(t *testing.T) {
	encoded := []byte{0xFF, 0xFE, 'H', 0x00, 'i', 0x00}
	decoded, err := DecodeBytes("UTF-16LE", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "Hi" {
		t.Fatalf("decoded = %q", decoded)
	}
	back, err := EncodeString("UTF-16LE", decoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) == 0 {
		t.Fatal("expected encoded bytes")
	}
}

func TestDecodeWindows1251(t *testing.T) {
	decoded, err := DecodeBytes("Windows-1251", []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2})
	if err != nil {
		t.Fatal(err)
	}
	if decoded != "Привет" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestStreamingReaderWriter(t *testing.T) {
	reader, err := NewDecoderReader("Windows-1252", bytes.NewReader([]byte("caf\xe9")))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "café" {
		t.Fatalf("decoded = %q", string(decoded))
	}

	var out bytes.Buffer
	writer, err := NewEncoderWriter("Windows-1252", &out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(writer.(io.Writer), strings.NewReader("café")); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatal("expected encoded output")
	}
}

func TestDecodeWindows1251UsesExpectedCodePoints(t *testing.T) {
	decoded, err := DecodeBytes("Windows-1251", []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2})
	if err != nil {
		t.Fatal(err)
	}
	want := "\u041f\u0440\u0438\u0432\u0435\u0442" // Привет
	if decoded != want {
		t.Fatalf("decoded = %q, want %q", decoded, want)
	}
}

func TestDecodeWindows1252UsesExpectedCodePoints(t *testing.T) {
	decoded, err := DecodeBytes("Windows-1252", []byte{0x63, 0x61, 0x66, 0xE9})
	if err != nil {
		t.Fatal(err)
	}
	want := "caf\u00e9"
	if decoded != want {
		t.Fatalf("decoded = %q, want %q", decoded, want)
	}
}

func TestEncodeWindows1251UsesExpectedBytes(t *testing.T) {
	encoded, err := EncodeString("Windows-1251", "\u041f\u0440\u0438\u0432\u0435\u0442")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2}
	if !bytes.Equal(encoded, want) {
		t.Fatalf("encoded = %s, want %s", hex.EncodeToString(encoded), hex.EncodeToString(want))
	}
}

func TestEncodeWindows1252UsesExpectedBytes(t *testing.T) {
	encoded, err := EncodeString("Windows-1252", "caf\u00e9")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x63, 0x61, 0x66, 0xE9}
	if !bytes.Equal(encoded, want) {
		t.Fatalf("encoded = %s, want %s", hex.EncodeToString(encoded), hex.EncodeToString(want))
	}
}
