package encodingx

import "testing"

var fuzzEncodingNames = []string{"UTF-8", "UTF-16LE", "UTF-16BE", "Windows-1251", "Windows-1252"}

func FuzzDecodeBestEffortAndDetect(f *testing.F) {
	f.Add([]byte("alpha\nbeta\n"))
	f.Add([]byte{0xEF, 0xBB, 0xBF, 'h', 'i'})
	f.Add([]byte{0xFF, 0xFE, 'h', 0x00, 'i', 0x00})
	f.Add([]byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2})

	f.Fuzz(func(t *testing.T, data []byte) {
		_ = DetectSample(data)
		for _, name := range fuzzEncodingNames {
			_ = DecodeBytesBestEffort(name, data)
		}
	})
}

func FuzzEncodeDecodeRoundTrip(f *testing.F) {
	f.Add("plain ascii")
	f.Add("caf\u00e9")
	f.Add("\u041f\u0440\u0438\u0432\u0435\u0442")
	f.Add("line one\nline two\r\n")

	f.Fuzz(func(t *testing.T, text string) {
		for _, name := range fuzzEncodingNames {
			encoded, err := EncodeString(name, text)
			if err != nil {
				continue
			}
			decoded, err := DecodeBytes(name, encoded)
			if err != nil {
				t.Fatalf("DecodeBytes(%s) after EncodeString failed: %v", name, err)
			}
			if decoded != text {
				t.Fatalf("%s round trip = %q, want %q", name, decoded, text)
			}
		}
	})
}
