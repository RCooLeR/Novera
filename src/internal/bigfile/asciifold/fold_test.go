package asciifold

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func TestLowerFoldsOnlyASCIIUppercase(t *testing.T) {
	tests := []struct {
		name string
		in   byte
		want byte
	}{
		{name: "uppercase", in: 'Q', want: 'q'},
		{name: "lowercase unchanged", in: 'q', want: 'q'},
		{name: "digit unchanged", in: '7', want: '7'},
		{name: "non ascii unchanged", in: 0xc4, want: 0xc4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Lower(tt.in); got != tt.want {
				t.Fatalf("Lower(%#x) = %#x, want %#x", tt.in, got, tt.want)
			}
		})
	}
}

func TestFoldPreservesByteLengthAndNonASCIIBytes(t *testing.T) {
	in := []byte("Straße KELVIN")
	got := Fold(in)
	if len(got) != len(in) {
		t.Fatalf("Fold changed byte length from %d to %d", len(in), len(got))
	}
	if string(got) != "straße Kelvin" {
		t.Fatalf("Fold = %q", got)
	}
}

func TestIndexFoldedUsesByteStableASCIIFold(t *testing.T) {
	haystack := []byte("abc KELVIN Kelvin")
	foldedNeedle := Fold([]byte("kelvin"))
	if got := IndexFolded(haystack, foldedNeedle); got != 4 {
		t.Fatalf("IndexFolded ASCII match = %d, want 4", got)
	}

	kelvinSignOffset := len("abc KELVIN ")
	if got := IndexFolded(haystack[kelvinSignOffset:], foldedNeedle); got != -1 {
		t.Fatalf("IndexFolded matched Unicode kelvin sign at %d", got)
	}
}

func TestIndexFoldedEmptyAndOversizedNeedles(t *testing.T) {
	if got := IndexFolded([]byte("abc"), nil); got != 0 {
		t.Fatalf("IndexFolded empty needle = %d, want 0", got)
	}
	if got := IndexFolded([]byte("abc"), []byte("abcd")); got != -1 {
		t.Fatalf("IndexFolded oversized needle = %d, want -1", got)
	}
}

func TestLastIndexFoldedUsesByteStableASCIIFold(t *testing.T) {
	haystack := []byte("Key one KEY \xe2\x84\xaa")
	foldedNeedle := Fold([]byte("key"))
	if got := LastIndexFolded(haystack, foldedNeedle); got != 8 {
		t.Fatalf("LastIndexFolded ASCII match = %d, want 8", got)
	}
	if got := LastIndexFolded(haystack[12:], Fold([]byte("k"))); got != -1 {
		t.Fatalf("LastIndexFolded matched Unicode kelvin sign at %d", got)
	}
}

func TestLastIndexFoldedEmptyAndOversizedNeedles(t *testing.T) {
	if got := LastIndexFolded([]byte("abc"), nil); got != 3 {
		t.Fatalf("LastIndexFolded empty needle = %d, want 3", got)
	}
	if got := LastIndexFolded([]byte("abc"), []byte("abcd")); got != -1 {
		t.Fatalf("LastIndexFolded oversized needle = %d, want -1", got)
	}
}

func TestFoldedSearchMatchesByteReference(t *testing.T) {
	random := rand.New(rand.NewPCG(17, 29))
	for trial := 0; trial < 500; trial++ {
		body := make([]byte, 512+random.IntN(512))
		needle := make([]byte, 32+random.IntN(128))
		for i := range body {
			body[i] = "AaAaB\xff"[random.IntN(6)]
		}
		for i := range needle {
			needle[i] = "aaaaab\xff"[random.IntN(7)]
		}
		if trial%2 == 0 {
			copy(body[random.IntN(len(body)-len(needle)+1):], needle)
		}
		folded := Fold(body)
		if got, want := IndexFolded(body, needle), bytes.Index(folded, needle); got != want {
			t.Fatalf("trial %d: forward=%d, want %d", trial, got, want)
		}
		if got, want := LastIndexFolded(body, needle), bytes.LastIndex(folded, needle); got != want {
			t.Fatalf("trial %d: backward=%d, want %d", trial, got, want)
		}
	}
}

func TestFoldedRepeatedPrefixFindsExactBoundaryMatches(t *testing.T) {
	needle := bytes.Repeat([]byte("a"), 64)
	needle[len(needle)-2] = 'b'
	for _, location := range []int{0, 8, 65, 128, 512 - len(needle)} {
		body := bytes.Repeat([]byte("A"), 512)
		copy(body[location:], bytes.ToUpper(needle))
		if got := IndexFolded(body, needle); got != location {
			t.Fatalf("forward=%d, want %d", got, location)
		}
		if got := LastIndexFolded(body, needle); got != location {
			t.Fatalf("backward=%d, want %d", got, location)
		}
	}
}
