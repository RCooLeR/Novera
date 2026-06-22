package asciifold

// Fold returns a byte-stable ASCII lowercase copy of b.
//
// It deliberately does not apply Unicode case folding: some Unicode folds change
// byte length, which makes byte-offset search and replacement unsafe.
func Fold(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = Lower(c)
	}
	return out
}

// Lower returns c folded to lowercase for ASCII A-Z only.
func Lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// IndexFolded returns the first byte offset of foldedNeedle in haystack using
// ASCII-only case folding. foldedNeedle must already be folded with Fold.
func IndexFolded(haystack []byte, foldedNeedle []byte) int {
	if len(foldedNeedle) == 0 {
		return 0
	}
	if len(foldedNeedle) > len(haystack) {
		return -1
	}
	first := foldedNeedle[0]
	limit := len(haystack) - len(foldedNeedle)
	for i := 0; i <= limit; i++ {
		if Lower(haystack[i]) != first {
			continue
		}
		if equalFoldedAt(haystack[i:i+len(foldedNeedle)], foldedNeedle) {
			return i
		}
	}
	return -1
}

func equalFoldedAt(haystack []byte, foldedNeedle []byte) bool {
	for i, c := range foldedNeedle {
		if Lower(haystack[i]) != c {
			return false
		}
	}
	return true
}
