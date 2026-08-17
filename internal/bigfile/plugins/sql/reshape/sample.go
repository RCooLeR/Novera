package reshape

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// ErrIncompleteStatement reports an unterminated SQL statement or literal in
// a bounded sample. Callers must not publish a guessed/truncated statement.
var ErrIncompleteStatement = errors.New("incomplete SQL statement")

// SampleInsertRows returns a syntactically complete prefix containing at most
// maxRows INSERT ... VALUES tuples. It uses the same strict INSERT grammar as
// reshape and scans later materialized bytes for unsafe syntax even after the
// requested output row count has been reached.
func SampleInsertRows(input []byte, maxRows int) (sample []byte, rows int, satisfied bool, err error) {
	if maxRows <= 0 {
		return nil, 0, false, errors.New("sample row limit must be positive")
	}

	var output bytes.Buffer
	statementStart := 0
	lex := lexer{}
	safety := reshapeSafetyGuard{}
	limitReached := false
	emit := func(end int) (bool, error) {
		statement := input[statementStart:end]
		parsed, isInsert, parseErr := parseInsert(statement)
		if parseErr != nil {
			return false, parseErr
		}
		if limitReached {
			return true, nil
		}
		if !isInsert {
			_, writeErr := output.Write(statement)
			return false, writeErr
		}
		remaining := maxRows - rows
		if remaining <= 0 {
			return true, nil
		}
		if len(parsed.tuples) <= remaining {
			if _, writeErr := output.Write(statement); writeErr != nil {
				return false, writeErr
			}
			rows += len(parsed.tuples)
			return rows >= maxRows, nil
		}
		if _, writeErr := output.Write(parsed.leading); writeErr != nil {
			return false, writeErr
		}
		if _, writeErr := output.WriteString(parsed.prefix); writeErr != nil {
			return false, writeErr
		}
		if _, writeErr := output.WriteString(strings.Join(parsed.tuples[:remaining], ",")); writeErr != nil {
			return false, writeErr
		}
		if _, writeErr := output.WriteString(";\n"); writeErr != nil {
			return false, writeErr
		}
		rows += remaining
		return true, nil
	}

	for index, current := range input {
		if guardErr := safety.Step(current); guardErr != nil {
			return nil, rows, false, guardErr
		}
		topLevel := lex.step(current)
		if !topLevel || lex.inLiteral() || current != ';' {
			continue
		}
		done, emitErr := emit(index + 1)
		if emitErr != nil {
			return nil, rows, false, emitErr
		}
		statementStart = index + 1
		lex = lexer{}
		if done {
			limitReached = true
		}
	}
	if guardErr := safety.Finish(); guardErr != nil {
		return nil, rows, false, guardErr
	}

	if lex.inLiteral() {
		return nil, rows, false, fmt.Errorf("%w: unterminated literal or comment", ErrIncompleteStatement)
	}
	if statementStart < len(input) {
		tail := input[statementStart:]
		if skipLeadingTrivia(tail) != len(tail) {
			_, isInsert, parseErr := parseInsert(tail)
			if parseErr != nil || isInsert {
				return nil, rows, false, fmt.Errorf("%w: missing statement terminator", ErrIncompleteStatement)
			}
		}
		if !limitReached {
			if _, writeErr := output.Write(tail); writeErr != nil {
				return nil, rows, false, writeErr
			}
		}
	}
	return output.Bytes(), rows, limitReached || rows >= maxRows, nil
}
