// Package newlines provides one encoding-aware streaming definition of CR,
// LF, and CRLF boundaries.
package newlines

// Break is one logical line terminator. Start points at CR or LF; End points
// immediately after the complete terminator.
type Break struct {
	Start int64
	End   int64
}

type encodingKind uint8

const (
	encodingSingleByte encodingKind = iota
	encodingUTF16LE
	encodingUTF16BE
)

// Scanner carries incomplete UTF-16 code units and a pending CR across chunks.
// Chunks must be contiguous absolute byte ranges.
type Scanner struct {
	kind encodingKind

	initialized bool
	nextOffset  int64

	haveUnitByte bool
	unitByte     byte
	unitOffset   int64

	pendingCR    bool
	pendingStart int64
	pendingEnd   int64
}

func New(encodingName string) *Scanner {
	kind := encodingSingleByte
	switch encodingName {
	case "UTF-16LE":
		kind = encodingUTF16LE
	case "UTF-16BE":
		kind = encodingUTF16BE
	}
	return &Scanner{kind: kind}
}

// Scan consumes a contiguous chunk at its absolute source offset.
func (s *Scanner) Scan(data []byte, baseOffset int64, emit func(Break) bool) bool {
	if s == nil {
		return true
	}
	if !s.initialized || baseOffset != s.nextOffset {
		s.reset(baseOffset)
	}
	s.nextOffset = baseOffset + int64(len(data))
	if s.kind == encodingSingleByte {
		return s.scanSingleByte(data, baseOffset, emit)
	}
	return s.scanUTF16(data, baseOffset, emit)
}

// Finish resolves a terminal standalone CR at true EOF.
func (s *Scanner) Finish(emit func(Break) bool) bool {
	if s == nil {
		return true
	}
	s.haveUnitByte = false
	if !s.pendingCR {
		return true
	}
	br := Break{Start: s.pendingStart, End: s.pendingEnd}
	s.pendingCR = false
	return emitBreak(emit, br)
}

func (s *Scanner) reset(baseOffset int64) {
	s.initialized = true
	s.nextOffset = baseOffset
	s.haveUnitByte = false
	s.pendingCR = false
}

func (s *Scanner) scanSingleByte(data []byte, baseOffset int64, emit func(Break) bool) bool {
	for i, value := range data {
		start := baseOffset + int64(i)
		if !s.processCodeUnit(uint16(value), start, start+1, emit) {
			return false
		}
	}
	return true
}

func (s *Scanner) scanUTF16(data []byte, baseOffset int64, emit func(Break) bool) bool {
	i := 0
	if s.haveUnitByte {
		if len(data) == 0 {
			return true
		}
		if baseOffset != s.unitOffset+1 {
			s.haveUnitByte = false
		} else {
			unit := s.decodeUnit(s.unitByte, data[0])
			s.haveUnitByte = false
			if !s.processCodeUnit(unit, s.unitOffset, baseOffset+1, emit) {
				return false
			}
			i = 1
		}
	}
	if i < len(data) && (baseOffset+int64(i))&1 != 0 {
		i++
	}
	for i+1 < len(data) {
		start := baseOffset + int64(i)
		unit := s.decodeUnit(data[i], data[i+1])
		if !s.processCodeUnit(unit, start, start+2, emit) {
			return false
		}
		i += 2
	}
	if i < len(data) {
		s.haveUnitByte = true
		s.unitByte = data[i]
		s.unitOffset = baseOffset + int64(i)
	}
	return true
}

func (s *Scanner) decodeUnit(first byte, second byte) uint16 {
	if s.kind == encodingUTF16BE {
		return uint16(first)<<8 | uint16(second)
	}
	return uint16(second)<<8 | uint16(first)
}

func (s *Scanner) processCodeUnit(unit uint16, start int64, end int64, emit func(Break) bool) bool {
	if s.pendingCR {
		if unit == '\n' {
			br := Break{Start: s.pendingStart, End: end}
			s.pendingCR = false
			return emitBreak(emit, br)
		}
		br := Break{Start: s.pendingStart, End: s.pendingEnd}
		s.pendingCR = false
		if !emitBreak(emit, br) {
			return false
		}
	}

	switch unit {
	case '\r':
		s.pendingCR = true
		s.pendingStart = start
		s.pendingEnd = end
	case '\n':
		return emitBreak(emit, Break{Start: start, End: end})
	}
	return true
}

func emitBreak(emit func(Break) bool, br Break) bool {
	return emit == nil || emit(br)
}

// First returns the first logical break in data. atEOF resolves a final CR.
func First(encodingName string, data []byte, baseOffset int64, atEOF bool) (Break, bool) {
	scanner := New(encodingName)
	var found Break
	ok := false
	completed := scanner.Scan(data, baseOffset, func(br Break) bool {
		found = br
		ok = true
		return false
	})
	if completed && !ok && atEOF {
		scanner.Finish(func(br Break) bool {
			found = br
			ok = true
			return false
		})
	}
	return found, ok
}

// Last returns the last logical break in data.
func Last(encodingName string, data []byte, baseOffset int64, atEOF bool) (Break, bool) {
	scanner := New(encodingName)
	var found Break
	ok := false
	scanner.Scan(data, baseOffset, func(br Break) bool {
		found = br
		ok = true
		return true
	})
	if atEOF {
		scanner.Finish(func(br Break) bool {
			found = br
			ok = true
			return true
		})
	}
	return found, ok
}
