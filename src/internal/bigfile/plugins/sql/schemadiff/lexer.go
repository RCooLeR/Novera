package schemadiff

import (
	"context"
	"fmt"
	"strings"
)

type sqlTokenKind uint8

const (
	tokenWord sqlTokenKind = iota + 1
	tokenQuotedIdentifier
	tokenString
	tokenNumber
	tokenSymbol
)

type sqlToken struct {
	kind  sqlTokenKind
	raw   string
	value string
	start int
	end   int
}

type lexedSQL struct {
	tokens  []sqlToken
	warning string
}

const schemaContextCheckInterval = 4 << 10

func lexDDL(ddl []byte) (lexedSQL, error) {
	return lexDDLContext(context.Background(), ddl)
}

func lexDDLContext(ctx context.Context, ddl []byte) (lexedSQL, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	input := string(ddl)
	result := lexedSQL{tokens: make([]sqlToken, 0, minInt(len(input)/3+1, MaxTokenCount))}
	for pos := 0; pos < len(input); {
		if err := schemaContextErrAt(ctx, pos); err != nil {
			return result, err
		}
		c := input[pos]
		if isSQLSpace(c) {
			pos++
			continue
		}
		if c == 0 {
			return result, fmt.Errorf("NUL byte at offset %d is unsupported in DDL", pos)
		}
		if c == '-' && pos+1 < len(input) && input[pos+1] == '-' && isDashCommentStart(input, pos+2) {
			var err error
			pos, err = skipLineComment(ctx, input, pos+2)
			if err != nil {
				return result, err
			}
			continue
		}
		if c == '#' {
			var err error
			pos, err = skipLineComment(ctx, input, pos+1)
			if err != nil {
				return result, err
			}
			continue
		}
		if c == '/' && pos+1 < len(input) && input[pos+1] == '*' {
			if pos+2 < len(input) && (input[pos+2] == '!' || input[pos+2] == '+') {
				return result, fmt.Errorf("executable or optimizer-hint comment at offset %d is unsupported", pos)
			}
			next, err := skipBlockComment(ctx, input, pos)
			if err != nil {
				return result, err
			}
			pos = next
			continue
		}

		var token sqlToken
		var next int
		var warning string
		var err error
		switch {
		case c == '\'':
			token, next, warning, err = scanStringToken(ctx, input, pos)
		case c == '`' || c == '"':
			token, next, warning, err = scanQuotedIdentifierToken(ctx, input, pos, c)
		case c == '[' || c == ']':
			return result, fmt.Errorf("bracket-quoted identifiers are unsupported at offset %d", pos)
		case c == '$' && dollarQuoteDelimiter(input, pos) != "":
			token, next, err = scanDollarStringToken(ctx, input, pos)
		case isWordStart(c):
			token, next, err = scanWordToken(input, pos)
		case isDigit(c):
			token, next, err = scanNumberToken(input, pos)
		default:
			token, next = scanSymbolToken(input, pos)
		}
		if err != nil {
			return result, err
		}
		if warning != "" && result.warning == "" {
			result.warning = warning
		}
		result.tokens = append(result.tokens, token)
		if len(result.tokens) > MaxTokenCount {
			return result, fmt.Errorf("token count exceeds limit of %d", MaxTokenCount)
		}
		pos = next
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func scanStringToken(ctx context.Context, input string, start int) (sqlToken, int, string, error) {
	warning := ""
	for pos := start + 1; pos < len(input); pos++ {
		if err := schemaContextErrAt(ctx, pos); err != nil {
			return sqlToken{}, 0, "", err
		}
		switch input[pos] {
		case '\'':
			if pos+1 < len(input) && input[pos+1] == '\'' {
				pos++
				continue
			}
			end := pos + 1
			if end-start > MaxDefinitionBytes {
				return sqlToken{}, 0, "", fmt.Errorf("string literal at offset %d exceeds %d-byte definition limit", start, MaxDefinitionBytes)
			}
			return sqlToken{kind: tokenString, raw: input[start:end], value: input[start:end], start: start, end: end}, end, warning, nil
		case '\\':
			if pos+1 < len(input) {
				if warning == "" {
					warning = fmt.Sprintf("backslash escape in string literal at offset %d depends on SQL mode", pos)
				}
				pos++
				continue
			}
		}
	}
	return sqlToken{}, 0, "", fmt.Errorf("unterminated string literal at offset %d", start)
}

func scanQuotedIdentifierToken(ctx context.Context, input string, start int, quote byte) (sqlToken, int, string, error) {
	var value strings.Builder
	value.Grow(minInt(len(input)-start, MaxIdentifierBytes))
	warning := ""
	for pos := start + 1; pos < len(input); pos++ {
		if err := schemaContextErrAt(ctx, pos); err != nil {
			return sqlToken{}, 0, "", err
		}
		c := input[pos]
		if c == quote {
			if pos+1 < len(input) && input[pos+1] == quote {
				if value.Len()+1 > MaxIdentifierBytes {
					return sqlToken{}, 0, "", fmt.Errorf("quoted identifier at offset %d exceeds %d bytes", start, MaxIdentifierBytes)
				}
				value.WriteByte(quote)
				pos++
				continue
			}
			end := pos + 1
			return sqlToken{kind: tokenQuotedIdentifier, raw: input[start:end], value: value.String(), start: start, end: end}, end, warning, nil
		}
		if c == '\\' && pos+1 < len(input) {
			if warning == "" {
				warning = fmt.Sprintf("backslash escape in quoted identifier at offset %d has dialect-dependent semantics", pos)
			}
			pos++
			c = input[pos]
		}
		if value.Len()+1 > MaxIdentifierBytes {
			return sqlToken{}, 0, "", fmt.Errorf("quoted identifier at offset %d exceeds %d bytes", start, MaxIdentifierBytes)
		}
		value.WriteByte(c)
	}
	return sqlToken{}, 0, "", fmt.Errorf("unterminated quoted identifier at offset %d", start)
}

func scanDollarStringToken(ctx context.Context, input string, start int) (sqlToken, int, error) {
	delimiter := dollarQuoteDelimiter(input, start)
	if delimiter == "" {
		return sqlToken{}, 0, fmt.Errorf("invalid dollar-quote delimiter at offset %d", start)
	}
	bodyStart := start + len(delimiter)
	bodyEnd := -1
	for pos := bodyStart; pos+len(delimiter) <= len(input); pos++ {
		if err := schemaContextErrAt(ctx, pos); err != nil {
			return sqlToken{}, 0, err
		}
		if strings.HasPrefix(input[pos:], delimiter) {
			bodyEnd = pos
			break
		}
	}
	if bodyEnd < 0 {
		return sqlToken{}, 0, fmt.Errorf("unterminated dollar-quoted string at offset %d", start)
	}
	end := bodyEnd + len(delimiter)
	if end-start > MaxDefinitionBytes {
		return sqlToken{}, 0, fmt.Errorf("dollar-quoted string at offset %d exceeds %d-byte definition limit", start, MaxDefinitionBytes)
	}
	return sqlToken{kind: tokenString, raw: input[start:end], value: input[start:end], start: start, end: end}, end, nil
}

func dollarQuoteDelimiter(input string, start int) string {
	if start >= len(input) || input[start] != '$' {
		return ""
	}
	for pos := start + 1; pos < len(input) && pos-start <= 65; pos++ {
		c := input[pos]
		if c == '$' {
			return input[start : pos+1]
		}
		if pos == start+1 && !(isASCIIAlpha(c) || c == '_') {
			return ""
		}
		if !(isASCIIAlpha(c) || isDigit(c) || c == '_') {
			return ""
		}
	}
	return ""
}

func scanWordToken(input string, start int) (sqlToken, int, error) {
	pos := start + 1
	for pos < len(input) && isWordContinue(input[pos]) {
		pos++
	}
	if pos-start > MaxIdentifierBytes {
		return sqlToken{}, 0, fmt.Errorf("word token at offset %d exceeds %d bytes", start, MaxIdentifierBytes)
	}
	raw := input[start:pos]
	if raw[0] == '$' {
		return sqlToken{}, 0, fmt.Errorf("unsupported dollar-prefixed token %q at offset %d", raw, start)
	}
	return sqlToken{kind: tokenWord, raw: raw, value: raw, start: start, end: pos}, pos, nil
}

func scanNumberToken(input string, start int) (sqlToken, int, error) {
	pos := start + 1
	for pos < len(input) {
		c := input[pos]
		if (c == '+' || c == '-') && pos > start && (input[pos-1] == 'e' || input[pos-1] == 'E') {
			pos++
			continue
		}
		if !(isASCIIAlpha(c) || isDigit(c) || c == '_' || c == '.' || c == '$') {
			break
		}
		pos++
	}
	if pos-start > MaxIdentifierBytes {
		return sqlToken{}, 0, fmt.Errorf("numeric token at offset %d exceeds %d bytes", start, MaxIdentifierBytes)
	}
	raw := input[start:pos]
	if !validSQLNumber(raw) {
		return sqlToken{}, 0, fmt.Errorf("malformed numeric token %q at offset %d", raw, start)
	}
	return sqlToken{kind: tokenNumber, raw: raw, value: raw, start: start, end: pos}, pos, nil
}

func validSQLNumber(value string) bool {
	if len(value) >= 3 && value[0] == '0' && (value[1] == 'x' || value[1] == 'X') {
		for i := 2; i < len(value); i++ {
			c := value[i]
			if !isDigit(c) && !(c >= 'a' && c <= 'f') && !(c >= 'A' && c <= 'F') {
				return false
			}
		}
		return true
	}
	if len(value) >= 3 && value[0] == '0' && (value[1] == 'b' || value[1] == 'B') {
		for i := 2; i < len(value); i++ {
			if value[i] != '0' && value[i] != '1' {
				return false
			}
		}
		return true
	}
	seenDot := false
	seenExponent := false
	exponentDigits := 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case isDigit(c):
			if seenExponent {
				exponentDigits++
			}
		case c == '.' && !seenDot && !seenExponent:
			seenDot = true
		case (c == 'e' || c == 'E') && !seenExponent:
			seenExponent = true
			if i+1 < len(value) && (value[i+1] == '+' || value[i+1] == '-') {
				i++
			}
		default:
			return false
		}
	}
	return !seenExponent || exponentDigits > 0
}

func scanSymbolToken(input string, start int) (sqlToken, int) {
	end := start + 1
	if end < len(input) {
		two := input[start : end+1]
		switch two {
		case "::", ">=", "<=", "<>", "!=", "||", "&&", ":=", "->", "=>", "@@", "#>":
			end++
		}
	}
	if end < len(input) {
		three := input[start : end+1]
		if three == "->>" || three == "#>>" {
			end++
		}
	}
	raw := input[start:end]
	return sqlToken{kind: tokenSymbol, raw: raw, value: raw, start: start, end: end}, end
}

func skipLineComment(ctx context.Context, input string, pos int) (int, error) {
	for pos < len(input) && input[pos] != '\n' && input[pos] != '\r' {
		if err := schemaContextErrAt(ctx, pos); err != nil {
			return 0, err
		}
		pos++
	}
	return pos, nil
}

func skipBlockComment(ctx context.Context, input string, start int) (int, error) {
	depth := 1
	for pos := start + 2; pos < len(input); pos++ {
		if err := schemaContextErrAt(ctx, pos); err != nil {
			return 0, err
		}
		if pos+1 < len(input) && input[pos] == '/' && input[pos+1] == '*' {
			depth++
			if depth > MaxNestingDepth {
				return 0, fmt.Errorf("block comment nesting exceeds limit of %d", MaxNestingDepth)
			}
			pos++
			continue
		}
		if pos+1 < len(input) && input[pos] == '*' && input[pos+1] == '/' {
			depth--
			pos++
			if depth == 0 {
				return pos + 1, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated block comment at offset %d", start)
}

func schemaContextErrAt(ctx context.Context, pos int) error {
	if pos%schemaContextCheckInterval != 0 {
		return nil
	}
	return ctx.Err()
}

func isDashCommentStart(input string, pos int) bool {
	return pos >= len(input) || isSQLSpace(input[pos])
}

func isSQLSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '\f', '\v':
		return true
	default:
		return false
	}
}

func isWordStart(c byte) bool {
	return isASCIIAlpha(c) || c == '_' || c == '$' || c >= 0x80
}

func isWordContinue(c byte) bool {
	return isWordStart(c) || isDigit(c)
}

func isASCIIAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
