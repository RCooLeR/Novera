package asciifold

import "testing"

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
