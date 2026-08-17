package schemadiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"unicode/utf8"
)

// ParseColumnsStrict preserves Novera's former column-only strict parser API
// while delegating to the complete bounded CREATE TABLE parser. Unsupported,
// ambiguous, truncated, or over-budget DDL is returned as an error instead of
// silently publishing a partial column set.
func ParseColumnsStrict(ddl []byte) ([]Column, error) {
	statement, err := firstCreateTableStatement(ddl)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseTableContext(context.Background(), statement)
	if err != nil {
		return nil, err
	}
	if parsed.Status != ParseComplete {
		if parsed.Reason == "" {
			return nil, errors.New("CREATE TABLE schema is unknown")
		}
		return nil, errors.New(parsed.Reason)
	}
	return parsed.Items, nil
}

// firstCreateTableStatement preserves the former API's ability to accept a
// bounded dump range containing a BOM, leading ordinary comments, and trailing
// SQL. Tokenization remains quote/comment aware; only the first live statement
// is delegated to the complete parser.
func firstCreateTableStatement(ddl []byte) ([]byte, error) {
	input := bytes.TrimPrefix(ddl, []byte{0xef, 0xbb, 0xbf})
	limit := len(input)
	if limit > MaxStatementBytes+1 {
		limit = MaxStatementBytes + 1
	}
	lexed, err := lexDDLContext(context.Background(), input[:limit])
	if err != nil {
		return nil, err
	}
	if len(lexed.tokens) == 0 {
		return nil, errors.New("expected CREATE TABLE statement")
	}
	start := lexed.tokens[0].start
	end := -1
	for _, token := range lexed.tokens {
		if token.raw == ";" {
			end = token.end
			break
		}
	}
	if end < 0 {
		if len(input) > MaxStatementBytes {
			return nil, fmt.Errorf("CREATE TABLE statement exceeds %d-byte limit", MaxStatementBytes)
		}
		return nil, errors.New("CREATE TABLE statement lacks a terminating semicolon")
	}
	if end-start > MaxStatementBytes {
		return nil, fmt.Errorf("CREATE TABLE statement exceeds %d-byte limit", MaxStatementBytes)
	}
	statement := input[start:end]
	if !utf8.Valid(statement) {
		return nil, errors.New("CREATE TABLE statement is not valid UTF-8")
	}
	return statement, nil
}
