package replace

import (
	"unicode"
	"unicode/utf8"
)

func replaceWordBoundaryOK(window []byte, pos int, length int, windowStart int64, size int64, wholeWord bool) bool {
	if !wholeWord {
		return true
	}
	beforeOK := pos == 0 && windowStart == 0
	if pos > 0 {
		beforeOK = !replaceIsWordRuneBefore(window[:pos])
	}
	after := pos + length
	afterOK := after >= len(window) && windowStart+int64(after) >= size
	if after < len(window) {
		afterOK = !replaceIsWordRuneAt(window[after:])
	}
	return beforeOK && afterOK
}

func replaceIsWordRuneBefore(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	r, width := utf8.DecodeLastRune(data)
	if r == utf8.RuneError && width == 1 {
		return true
	}
	return replaceIsWordRune(r)
}

func replaceIsWordRuneAt(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	r, width := utf8.DecodeRune(data)
	if r == utf8.RuneError && width == 1 {
		return true
	}
	return replaceIsWordRune(r)
}

func replaceIsWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}
