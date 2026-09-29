// Package delimiter detects mysql-client DELIMITER directives in a streaming
// SQL source. DELIMITER is not SQL syntax: it changes how the client divides
// routine bodies into statements, so SQL transforms and offset analyzers that
// only understand ordinary semicolon boundaries must reject it conservatively.
package delimiter

import "errors"

// ErrUnsupported is returned when a mysql-client DELIMITER directive makes a
// dump unsafe for ordinary semicolon-based analysis or transformation.
var ErrUnsupported = errors.New("custom SQL DELIMITER directives are not supported because routine bodies cannot be safely parsed")

const keyword = "delimiter"

type lineState uint8

const (
	lineLeading lineState = iota
	lineKeyword
	lineBoundary
	lineIgnored
)

// Detector recognizes the mysql-client DELIMITER command at the start of any
// physical line. Its state is fixed-size: indentation can be arbitrarily long,
// input may arrive one byte at a time, and no source line is retained. A UTF-8
// BOM is accepted only at the beginning of the source.
//
// Detection is deliberately independent of an SQL lexer. Once a custom
// delimiter begins a routine body, an ordinary semicolon lexer can enter a
// misleading state at an internal semicolon. Looking only at bytes that such a
// lexer considers "normal" could therefore miss the directive that makes all
// subsequently derived statement boundaries unsafe.
type Detector struct {
	lineState lineState
	keywordAt uint8
	bomState  uint8 // 0: undecided; 1: EF; 2: EF BB; 3: resolved
}

// Step consumes one source byte and reports whether a directive has been
// recognized. Callers may stop immediately after it returns true.
func (d *Detector) Step(c byte) bool {
	if d.bomState != 3 {
		switch d.bomState {
		case 0:
			if c == 0xef {
				d.bomState = 1
				return false
			}
			d.bomState = 3
			return d.stepLine(c)
		case 1:
			if c == 0xbb {
				d.bomState = 2
				return false
			}
			d.bomState = 3
			return d.stepLine(0xef) || d.stepLine(c)
		case 2:
			d.bomState = 3
			if c == 0xbf {
				return false
			}
			return d.stepLine(0xef) || d.stepLine(0xbb) || d.stepLine(c)
		}
	}
	return d.stepLine(c)
}

func (d *Detector) stepLine(c byte) bool {
	if c == '\r' || c == '\n' {
		matched := d.lineState == lineBoundary
		d.lineState = lineLeading
		d.keywordAt = 0
		return matched
	}

	switch d.lineState {
	case lineLeading:
		if isHorizontalSpace(c) {
			return false
		}
		if lower(c) == keyword[0] {
			d.lineState = lineKeyword
			d.keywordAt = 1
			return false
		}
		d.lineState = lineIgnored
	case lineKeyword:
		if lower(c) != keyword[d.keywordAt] {
			d.lineState = lineIgnored
			return false
		}
		d.keywordAt++
		if int(d.keywordAt) == len(keyword) {
			d.lineState = lineBoundary
		}
	case lineBoundary:
		if !isWordByte(c) {
			return true
		}
		d.lineState = lineIgnored
	}
	return false
}

// Finish reports a directive whose keyword ends exactly at EOF.
func (d *Detector) Finish() bool {
	return d.bomState == 3 && d.lineState == lineBoundary
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

func isHorizontalSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\f' || c == '\v'
}

func isWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}
