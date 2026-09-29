package highlight

import (
	"strings"

	basehighlight "novera/internal/bigfile/highlight"
)

type TokenKind = basehighlight.TokenKind
type Token = basehighlight.Token

type Dialect string

const (
	DialectMySQL Dialect = "mysql"
	DialectANSI  Dialect = "ansi"
)

const (
	TokenText       = basehighlight.TokenText
	TokenKeyword    = basehighlight.TokenKeyword
	TokenString     = basehighlight.TokenString
	TokenComment    = basehighlight.TokenComment
	TokenNumber     = basehighlight.TokenNumber
	TokenIdentifier = basehighlight.TokenIdentifier
	TokenOperator   = basehighlight.TokenOperator
)

var sqlKeywords = map[string]bool{
	"ADD": true, "ALTER": true, "AND": true, "AS": true, "AUTO_INCREMENT": true,
	"BY": true, "CHARACTER": true, "CHARSET": true, "COLLATE": true, "COMMENT": true,
	"CONSTRAINT": true, "CREATE": true, "DATABASE": true, "DEFAULT": true, "DELETE": true,
	"DROP": true, "ENGINE": true, "EXISTS": true, "FROM": true, "IF": true,
	"IN": true, "INDEX": true, "INSERT": true, "INTO": true, "KEY": true,
	"NOT": true, "NULL": true, "ON": true, "OR": true, "ORDER": true,
	"PRIMARY": true, "REFERENCES": true, "REPLACE": true, "SELECT": true, "SET": true,
	"TABLE": true, "UNIQUE": true, "UPDATE": true, "USE": true, "VALUES": true,
	"WHERE": true,
}

// SQLVisible lexes a bounded visible SQL slice into lightweight highlight tokens.
// It intentionally avoids full-file or multi-line context and only tokenizes what
// is currently visible inside the viewport.
func SQLVisible(text string) []Token {
	return SQLVisibleDialect(text, DialectMySQL)
}

// SQLVisibleDialect tokenizes a bounded visible SQL slice with explicit
// line-comment rules. Novera's dump workbench defaults to MySQL semantics:
// '#' starts a comment, while '--' requires following whitespace/control so
// subtraction-like text such as "1--2" is not swallowed.
func SQLVisibleDialect(text string, dialect Dialect) []Token {
	tokens := make([]Token, 0, len(text)/8)
	flushText := func(start, end int) {
		if end > start {
			tokens = append(tokens, Token{Start: start, End: end, Kind: TokenText})
		}
	}

	i := 0
	textStart := 0
	for i < len(text) {
		switch {
		case startsSQLLineComment(text, i, dialect):
			flushText(textStart, i)
			end := scanSQLLineComment(text, i)
			tokens = append(tokens, Token{Start: i, End: end, Kind: TokenComment})
			i = end
			textStart = i
			continue
		case hasPrefixAt(text, i, "/*"):
			flushText(textStart, i)
			end := strings.Index(text[i+2:], "*/")
			if end >= 0 {
				end += i + 4
			} else {
				end = len(text)
			}
			tokens = append(tokens, Token{Start: i, End: end, Kind: TokenComment})
			i = end
			textStart = i
			continue
		case text[i] == '\'' || text[i] == '"':
			flushText(textStart, i)
			end := scanSQLString(text, i)
			tokens = append(tokens, Token{Start: i, End: end, Kind: TokenString})
			i = end
			textStart = i
			continue
		case text[i] == '`':
			flushText(textStart, i)
			end := scanBacktickIdentifier(text, i)
			tokens = append(tokens, Token{Start: i, End: end, Kind: TokenIdentifier})
			i = end
			textStart = i
			continue
		case isDigit(text[i]):
			if i > 0 && isWordByte(text[i-1]) {
				i++
				continue
			}
			flushText(textStart, i)
			end := scanNumber(text, i)
			tokens = append(tokens, Token{Start: i, End: end, Kind: TokenNumber})
			i = end
			textStart = i
			continue
		case isWordByte(text[i]):
			start := i
			for i < len(text) && isWordByte(text[i]) {
				i++
			}
			word := strings.ToUpper(text[start:i])
			if sqlKeywords[word] {
				flushText(textStart, start)
				tokens = append(tokens, Token{Start: start, End: i, Kind: TokenKeyword})
				textStart = i
			}
			continue
		case isOperatorByte(text[i]):
			flushText(textStart, i)
			tokens = append(tokens, Token{Start: i, End: i + 1, Kind: TokenOperator})
			i++
			textStart = i
			continue
		default:
			i++
		}
	}

	flushText(textStart, len(text))
	return mergeAdjacentTextTokens(tokens)
}

func startsSQLLineComment(text string, index int, dialect Dialect) bool {
	if index < 0 || index >= len(text) {
		return false
	}
	if text[index] == '#' {
		return dialect != DialectANSI
	}
	if !hasPrefixAt(text, index, "--") {
		return false
	}
	if dialect == DialectANSI {
		return true
	}
	after := index + 2
	return after >= len(text) || text[after] <= ' '
}

func scanSQLLineComment(text string, start int) int {
	for i := start; i < len(text); i++ {
		if text[i] == '\r' || text[i] == '\n' {
			return i
		}
	}
	return len(text)
}

func mergeAdjacentTextTokens(tokens []Token) []Token {
	if len(tokens) < 2 {
		return tokens
	}
	merged := make([]Token, 0, len(tokens))
	for _, tok := range tokens {
		if len(merged) == 0 {
			merged = append(merged, tok)
			continue
		}
		last := &merged[len(merged)-1]
		if last.Kind == TokenText && tok.Kind == TokenText && last.End == tok.Start {
			last.End = tok.End
			continue
		}
		merged = append(merged, tok)
	}
	return merged
}

func scanSQLString(text string, start int) int {
	quote := text[start]
	i := start + 1
	for i < len(text) {
		if text[i] == '\\' && i+1 < len(text) {
			i += 2
			continue
		}
		if text[i] == quote {
			if i+1 < len(text) && text[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(text)
}

func scanBacktickIdentifier(text string, start int) int {
	i := start + 1
	for i < len(text) {
		if text[i] == '`' {
			if i+1 < len(text) && text[i+1] == '`' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(text)
}

func scanNumber(text string, start int) int {
	i := start
	dotSeen := false
	for i < len(text) {
		switch {
		case isDigit(text[i]):
			i++
		case text[i] == '.' && !dotSeen:
			dotSeen = true
			i++
		default:
			return i
		}
	}
	return i
}

func hasPrefixAt(text string, index int, prefix string) bool {
	return index+len(prefix) <= len(text) && text[index:index+len(prefix)] == prefix
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_'
}

func isOperatorByte(b byte) bool {
	switch b {
	case '(', ')', ',', ';', '=', '+', '-', '*', '/', '%', '<', '>', '!':
		return true
	default:
		return false
	}
}
