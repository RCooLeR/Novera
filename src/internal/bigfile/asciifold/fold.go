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
	failures := 0
	for i := 0; i <= limit; i++ {
		if Lower(haystack[i]) != first {
			continue
		}
		if equalFoldedAt(haystack[i:i+len(foldedNeedle)], foldedNeedle) {
			return i
		}
		failures++
		if len(foldedNeedle) >= 32 && failures >= 8 {
			if index := indexFoldedRolling(haystack[i+1:], foldedNeedle, false); index >= 0 {
				return i + 1 + index
			}
			return -1
		}
	}
	return -1
}

// LastIndexFolded returns the last byte offset of foldedNeedle in haystack
// using ASCII-only case folding. foldedNeedle must already be folded with Fold.
func LastIndexFolded(haystack []byte, foldedNeedle []byte) int {
	if len(foldedNeedle) == 0 {
		return len(haystack)
	}
	if len(foldedNeedle) > len(haystack) {
		return -1
	}
	first := foldedNeedle[0]
	failures := 0
	for i := len(haystack) - len(foldedNeedle); i >= 0; i-- {
		if Lower(haystack[i]) != first {
			continue
		}
		if equalFoldedAt(haystack[i:i+len(foldedNeedle)], foldedNeedle) {
			return i
		}
		failures++
		if len(foldedNeedle) >= 32 && failures >= 8 {
			return indexFoldedRolling(haystack[:i+len(foldedNeedle)-1], foldedNeedle, true)
		}
	}
	return -1
}

// Repeated near-matches make the simple candidate scan quadratic in the needle
// length. Switch after a few failures to a rolling hash, keeping the common
// short-needle/early-hit path cheap and requiring no folded source allocation.
// Every hash match is checked byte-for-byte, so collisions cannot change results.
func indexFoldedRolling(haystack, needle []byte, backward bool) int {
	n := len(needle)
	if n > len(haystack) {
		return -1
	}
	const prime uint64 = 16777619
	var wanted, current uint64
	power := uint64(1)
	if backward {
		start := len(haystack) - n
		for i := n - 1; i >= 0; i-- {
			wanted = wanted*prime + uint64(needle[i])
			current = current*prime + uint64(Lower(haystack[start+i]))
			power *= prime
		}
		if current == wanted && equalFoldedAt(haystack[start:], needle) {
			return start
		}
		for start--; start >= 0; start-- {
			current = current*prime + uint64(Lower(haystack[start])) - power*uint64(Lower(haystack[start+n]))
			if current == wanted && equalFoldedAt(haystack[start:start+n], needle) {
				return start
			}
		}
		return -1
	}
	for i, c := range needle {
		wanted = wanted*prime + uint64(c)
		current = current*prime + uint64(Lower(haystack[i]))
		power *= prime
	}
	if current == wanted && equalFoldedAt(haystack[:n], needle) {
		return 0
	}
	for end := n; end < len(haystack); end++ {
		current = current*prime + uint64(Lower(haystack[end])) - power*uint64(Lower(haystack[end-n]))
		start := end - n + 1
		if current == wanted && equalFoldedAt(haystack[start:end+1], needle) {
			return start
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
