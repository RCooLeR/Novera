package workspace

import "testing"

func TestDecodeText(t *testing.T) {
	utf16le := func(s string) []byte {
		b, _ := encodeText(s, encUTF16LE)
		return b
	}
	utf16be := func(s string) []byte {
		b, _ := encodeText(s, encUTF16BE)
		return b
	}
	cases := []struct {
		name    string
		data    []byte
		wantTxt string
		wantEnc string
		wantOK  bool
	}{
		{"plain utf8", []byte("hello, мир"), "hello, мир", encUTF8, true},
		{"utf8 bom", append([]byte{0xEF, 0xBB, 0xBF}, []byte("café")...), "café", encUTF8BOM, true},
		{"utf16le bom", utf16le("Привет"), "Привет", encUTF16LE, true},
		{"utf16be bom", utf16be("Привет"), "Привет", encUTF16BE, true},
		{"latin1", []byte{'c', 'a', 'f', 0xE9}, "café", encLatin1, true}, // 0xE9 = é in latin-1
		{"binary", []byte{0x00, 0x01, 0x02, 'A', 'B', 0x00, 0x03}, "", "", false},
		{"bomless utf16le ascii", []byte{'h', 0, 'i', 0, '!', 0, '\n', 0}, "hi!\n", encUTF16LE, true},
	}
	for _, c := range cases {
		txt, enc, ok := decodeText(c.data)
		if ok != c.wantOK || (ok && (txt != c.wantTxt || enc != c.wantEnc)) {
			t.Errorf("%s: decodeText = (%q, %q, %v), want (%q, %q, %v)", c.name, txt, enc, ok, c.wantTxt, c.wantEnc, c.wantOK)
		}
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	for _, enc := range []string{encUTF8, encUTF8BOM, encUTF16LE, encUTF16BE, encLatin1} {
		src := "abc"
		if enc != encLatin1 {
			src = "abc — Σ ✓" // non-latin-1 chars only where the encoding supports them
		}
		b, err := encodeText(src, enc)
		if err != nil {
			t.Fatalf("encode %s: %v", enc, err)
		}
		got, gotEnc, ok := decodeText(b)
		if !ok || got != src {
			t.Errorf("round-trip %s: got (%q, %q, %v), want %q", enc, got, gotEnc, ok, src)
		}
	}
}

func TestEncodeLatin1Lossy(t *testing.T) {
	// A character outside latin-1 must fail loudly rather than corrupt the file.
	if _, err := encodeText("a—b", encLatin1); err == nil {
		t.Error("expected error encoding non-latin-1 char as latin-1, got nil")
	}
}
