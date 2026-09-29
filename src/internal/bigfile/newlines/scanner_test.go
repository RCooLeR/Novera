package newlines

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func TestSingleByteCRLFIsOneBreakAcrossEverySeam(t *testing.T) {
	data := []byte("a\r\nb\rc\nd\r")
	want := []Break{{Start: 1, End: 3}, {Start: 4, End: 5}, {Start: 6, End: 7}, {Start: 8, End: 9}}
	for split := 0; split <= len(data); split++ {
		scanner := New("UTF-8")
		var got []Break
		scanner.Scan(data[:split], 0, func(br Break) bool { got = append(got, br); return true })
		scanner.Scan(data[split:], int64(split), func(br Break) bool { got = append(got, br); return true })
		scanner.Finish(func(br Break) bool { got = append(got, br); return true })
		assertBreaks(t, split, got, want)
	}
}

func TestUTF16BreaksAreAlignedAndSeamInvariant(t *testing.T) {
	for _, encoding := range []string{"UTF-16LE", "UTF-16BE"} {
		t.Run(encoding, func(t *testing.T) {
			data := encodeUTF16(encoding, []rune{'a', '\r', '\n', 'b', '\r', 'c', '\n', 'd', '\r'})
			want := []Break{{Start: 2, End: 6}, {Start: 8, End: 10}, {Start: 12, End: 14}, {Start: 16, End: 18}}
			for split := 0; split <= len(data); split++ {
				scanner := New(encoding)
				var got []Break
				scanner.Scan(data[:split], 0, func(br Break) bool { got = append(got, br); return true })
				scanner.Scan(data[split:], int64(split), func(br Break) bool { got = append(got, br); return true })
				scanner.Finish(func(br Break) bool { got = append(got, br); return true })
				assertBreaks(t, split, got, want)
				for _, br := range got {
					if br.Start&1 != 0 || br.End&1 != 0 {
						t.Fatalf("split %d produced unaligned break %+v", split, br)
					}
				}
			}
		})
	}
}

func TestUTF16IgnoresUnalignedNewlineBytePatterns(t *testing.T) {
	tests := []struct {
		encoding string
		data     []byte
	}{
		{encoding: "UTF-16LE", data: []byte{0x00, 0x0A, 0x00, 0x0D, 0x00, 0x41}},
		{encoding: "UTF-16BE", data: []byte{0x0A, 0x00, 0x0D, 0x00, 0x41, 0x00}},
	}
	for _, tt := range tests {
		t.Run(tt.encoding, func(t *testing.T) {
			scanner := New(tt.encoding)
			var got []Break
			scanner.Scan(tt.data, 0, func(br Break) bool { got = append(got, br); return true })
			scanner.Finish(func(br Break) bool { got = append(got, br); return true })
			if len(got) != 0 {
				t.Fatalf("unaligned patterns produced breaks: %+v", got)
			}
		})
	}
}

func encodeUTF16(encoding string, runes []rune) []byte {
	units := utf16.Encode(runes)
	out := make([]byte, len(units)*2)
	for i, unit := range units {
		if encoding == "UTF-16BE" {
			binary.BigEndian.PutUint16(out[i*2:], unit)
		} else {
			binary.LittleEndian.PutUint16(out[i*2:], unit)
		}
	}
	return out
}

func assertBreaks(t *testing.T, split int, got []Break, want []Break) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("split %d: breaks = %+v, want %+v", split, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("split %d: break %d = %+v, want %+v", split, i, got[i], want[i])
		}
	}
}
