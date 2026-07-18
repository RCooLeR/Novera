// Package sqlguard enforces read-only, single-statement SQL. It is ported
// verbatim from the prior generation's sqlguard (well-tested there): a
// comment/quote-aware tokenizer rejects multiple statements and any mutating or
// engine-specific dangerous construct, paired at the DB layer with read-only
// transactions for defense in depth.
package sqlguard

import (
	"errors"
	"strings"
	"unicode"
)

// SupportedKinds is the single source of truth for the database engines the app
// supports. The db layer validates profile kinds against this list and the
// per-engine blocklist below covers exactly these kinds (see the guard test).
var SupportedKinds = []string{"sqlite", "postgres", "mysql"}

type Options struct {
	UnsupportedMessage string
	BlockedMessage     string
	EmptyMessage       string
	Kind               string
	AllowWith          bool
}

func NormalizeReadOnly(query string, options Options) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New(firstNonEmpty(options.EmptyMessage, "enter a read-only SELECT query"))
	}
	analysis := analyze(query)
	if err := validateSingleStatement(analysis, options.EmptyMessage); err != nil {
		return "", err
	}
	if len(analysis.tokens) == 0 || !allowedFirstToken(analysis.tokens[0], options.AllowWith) {
		return "", errors.New(firstNonEmpty(options.UnsupportedMessage, "only read-only SELECT queries are supported"))
	}
	if containsBlockedSQLForKind(analysis, options.Kind) {
		return "", errors.New(firstNonEmpty(options.BlockedMessage, "mutating SQL is blocked"))
	}
	// Execute comment-free text so what reaches the engine matches what the guard
	// tokenised — a comment can't carry a second meaning past the analysis.
	cleaned := StripComments(query)
	for strings.HasSuffix(cleaned, ";") {
		cleaned = strings.TrimSpace(strings.TrimSuffix(cleaned, ";"))
	}
	if cleaned == "" {
		return "", errors.New(firstNonEmpty(options.EmptyMessage, "enter a read-only SELECT query"))
	}
	return cleaned, nil
}

func StripComments(query string) string {
	runes := []rune(query)
	var builder strings.Builder
	lineComment := false
	blockCommentDepth := 0
	inSingleQuote := false
	inDoubleQuote := false
	inBacktickQuote := false
	inBracketQuote := false
	dollarQuoteTag := ""

	for index := 0; index < len(runes); index++ {
		current := runes[index]
		if lineComment {
			if current == '\n' {
				lineComment = false
				builder.WriteRune(current)
			}
			continue
		}
		if blockCommentDepth > 0 {
			if current == '/' && index+1 < len(runes) && runes[index+1] == '*' {
				blockCommentDepth++
				index++
				continue
			}
			if current == '*' && index+1 < len(runes) && runes[index+1] == '/' {
				blockCommentDepth--
				index++
				if blockCommentDepth == 0 {
					builder.WriteRune(' ')
				}
			}
			continue
		}
		if dollarQuoteTag != "" {
			builder.WriteRune(current)
			if hasRunePrefix(runes, index, []rune(dollarQuoteTag)) {
				for offset := 1; offset < len([]rune(dollarQuoteTag)); offset++ {
					builder.WriteRune(runes[index+offset])
				}
				index += len([]rune(dollarQuoteTag)) - 1
				dollarQuoteTag = ""
			}
			continue
		}
		if inSingleQuote {
			builder.WriteRune(current)
			if current == '\'' {
				if index+1 < len(runes) && runes[index+1] == '\'' {
					index++
					builder.WriteRune(runes[index])
				} else {
					inSingleQuote = false
				}
			}
			continue
		}
		if inDoubleQuote {
			builder.WriteRune(current)
			if current == '"' {
				if index+1 < len(runes) && runes[index+1] == '"' {
					index++
					builder.WriteRune(runes[index])
				} else {
					inDoubleQuote = false
				}
			}
			continue
		}
		if inBacktickQuote {
			builder.WriteRune(current)
			if current == '`' {
				if index+1 < len(runes) && runes[index+1] == '`' {
					index++
					builder.WriteRune(runes[index])
				} else {
					inBacktickQuote = false
				}
			}
			continue
		}
		if inBracketQuote {
			builder.WriteRune(current)
			if current == ']' {
				if index+1 < len(runes) && runes[index+1] == ']' {
					index++
					builder.WriteRune(runes[index])
				} else {
					inBracketQuote = false
				}
			}
			continue
		}

		switch current {
		case '#':
			lineComment = true
		case '-':
			if index+1 < len(runes) && runes[index+1] == '-' {
				lineComment = true
				index++
				continue
			}
			builder.WriteRune(current)
		case '/':
			if index+1 < len(runes) && runes[index+1] == '*' {
				blockCommentDepth = 1
				index++
				continue
			}
			builder.WriteRune(current)
		case '\'':
			inSingleQuote = true
			builder.WriteRune(current)
		case '"':
			inDoubleQuote = true
			builder.WriteRune(current)
		case '`':
			inBacktickQuote = true
			builder.WriteRune(current)
		case '[':
			inBracketQuote = true
			builder.WriteRune(current)
		case '$':
			if marker, nextIndex, ok := parseDollarQuoteStart(runes, index); ok {
				dollarQuoteTag = marker
				for offset := index; offset < nextIndex; offset++ {
					builder.WriteRune(runes[offset])
				}
				index = nextIndex - 1
				continue
			}
			builder.WriteRune(current)
		default:
			builder.WriteRune(current)
		}
	}
	return strings.TrimSpace(builder.String())
}

type analysis struct {
	tokens           []string
	tokenInfos       []sqlToken
	statementCount   int
	invalidStatement bool
}

type sqlToken struct {
	text      string
	qualified bool
	called    bool
	quoted    bool
}

func analyze(query string) analysis {
	var result analysis
	runes := []rune(query)
	var tokenBuilder strings.Builder
	currentTokenQuoted := false
	currentHasContent := false
	nextTokenQualified := false
	lastTokenIndex := -1
	afterTokenOnlySpace := false
	lineComment := false
	blockCommentDepth := 0
	inSingleQuote := false
	inDoubleQuote := false
	inBacktickQuote := false
	inBracketQuote := false
	dollarQuoteTag := ""

	flushToken := func() {
		if tokenBuilder.Len() == 0 {
			currentTokenQuoted = false
			return
		}
		token := strings.ToLower(tokenBuilder.String())
		result.tokens = append(result.tokens, token)
		result.tokenInfos = append(result.tokenInfos, sqlToken{text: token, qualified: nextTokenQualified, quoted: currentTokenQuoted})
		lastTokenIndex = len(result.tokenInfos) - 1
		tokenBuilder.Reset()
		currentTokenQuoted = false
		nextTokenQualified = false
		currentHasContent = true
		afterTokenOnlySpace = true
	}
	markCallIfPreviousToken := func() {
		if afterTokenOnlySpace && lastTokenIndex >= 0 {
			result.tokenInfos[lastTokenIndex].called = true
		}
		afterTokenOnlySpace = false
	}

	for index := 0; index < len(runes); index++ {
		current := runes[index]
		if lineComment {
			if current == '\n' {
				lineComment = false
			}
			continue
		}
		if blockCommentDepth > 0 {
			if current == '/' && index+1 < len(runes) && runes[index+1] == '*' {
				blockCommentDepth++
				index++
				continue
			}
			if current == '*' && index+1 < len(runes) && runes[index+1] == '/' {
				blockCommentDepth--
				index++
			}
			continue
		}
		if dollarQuoteTag != "" {
			if hasRunePrefix(runes, index, []rune(dollarQuoteTag)) {
				index += len([]rune(dollarQuoteTag)) - 1
				dollarQuoteTag = ""
			}
			continue
		}
		if inSingleQuote {
			if current == '\'' {
				if index+1 < len(runes) && runes[index+1] == '\'' {
					index++
				} else {
					inSingleQuote = false
				}
			}
			continue
		}
		if inDoubleQuote {
			if current == '"' {
				if index+1 < len(runes) && runes[index+1] == '"' {
					tokenBuilder.WriteRune(current)
					index++
				} else {
					inDoubleQuote = false
					flushToken()
				}
			} else {
				tokenBuilder.WriteRune(current)
			}
			continue
		}
		if inBacktickQuote {
			if current == '`' {
				if index+1 < len(runes) && runes[index+1] == '`' {
					tokenBuilder.WriteRune(current)
					index++
				} else {
					inBacktickQuote = false
					flushToken()
				}
			} else {
				tokenBuilder.WriteRune(current)
			}
			continue
		}
		if inBracketQuote {
			if current == ']' {
				if index+1 < len(runes) && runes[index+1] == ']' {
					tokenBuilder.WriteRune(current)
					index++
				} else {
					inBracketQuote = false
					flushToken()
				}
			} else {
				tokenBuilder.WriteRune(current)
			}
			continue
		}

		switch current {
		case '#':
			// A comment is whitespace for call-detection: do NOT reset
			// afterTokenOnlySpace, so `func /*x*/ (` still counts as a call and
			// dangerous functions can't hide behind a comment before "(".
			flushToken()
			lineComment = true
		case '-':
			if index+1 < len(runes) && runes[index+1] == '-' {
				flushToken()
				lineComment = true
				index++
				continue
			}
			flushToken()
			afterTokenOnlySpace = false
			currentHasContent = true
		case '/':
			if index+1 < len(runes) && runes[index+1] == '*' {
				flushToken()
				blockCommentDepth = 1
				index++
				continue
			}
			flushToken()
			afterTokenOnlySpace = false
			currentHasContent = true
		case '\'':
			flushToken()
			afterTokenOnlySpace = false
			inSingleQuote = true
			currentHasContent = true
		case '"':
			flushToken()
			afterTokenOnlySpace = false
			currentTokenQuoted = true
			inDoubleQuote = true
			currentHasContent = true
		case '`':
			flushToken()
			afterTokenOnlySpace = false
			currentTokenQuoted = true
			inBacktickQuote = true
			currentHasContent = true
		case '[':
			flushToken()
			afterTokenOnlySpace = false
			currentTokenQuoted = true
			inBracketQuote = true
			currentHasContent = true
		case '$':
			if marker, nextIndex, ok := parseDollarQuoteStart(runes, index); ok {
				flushToken()
				afterTokenOnlySpace = false
				dollarQuoteTag = marker
				currentHasContent = true
				index = nextIndex - 1
				continue
			}
			tokenBuilder.WriteRune(current)
		case ';':
			flushToken()
			afterTokenOnlySpace = false
			if !currentHasContent {
				result.invalidStatement = true
				continue
			}
			result.statementCount++
			currentHasContent = false
		default:
			if isSQLWordRune(current) {
				tokenBuilder.WriteRune(current)
				afterTokenOnlySpace = false
				continue
			}
			flushToken()
			if current == '(' {
				markCallIfPreviousToken()
			} else if !unicode.IsSpace(current) {
				afterTokenOnlySpace = false
			}
			if current == '.' {
				nextTokenQualified = true
			}
			if !unicode.IsSpace(current) {
				currentHasContent = true
			}
		}
	}
	flushToken()
	if currentHasContent {
		result.statementCount++
	}
	return result
}

func validateSingleStatement(analysis analysis, emptyMessage string) error {
	if analysis.invalidStatement {
		return errors.New("query must contain a single SQL statement")
	}
	if analysis.statementCount == 0 {
		return errors.New(firstNonEmpty(emptyMessage, "enter a read-only SELECT query"))
	}
	if analysis.statementCount > 1 {
		return errors.New("query must contain a single SQL statement")
	}
	return nil
}

func allowedFirstToken(token string, allowWith bool) bool {
	return token == "select" || (allowWith && token == "with")
}

func hasRunePrefix(runes []rune, start int, prefix []rune) bool {
	if start+len(prefix) > len(runes) {
		return false
	}
	for index := range prefix {
		if runes[start+index] != prefix[index] {
			return false
		}
	}
	return true
}

func parseDollarQuoteStart(runes []rune, start int) (string, int, bool) {
	if start >= len(runes) || runes[start] != '$' {
		return "", 0, false
	}
	end := start + 1
	for end < len(runes) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end]) || runes[end] == '_') {
		end++
	}
	if end < len(runes) && runes[end] == '$' {
		return string(runes[start : end+1]), end + 1, true
	}
	return "", 0, false
}

func isSQLWordRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsDigit(value) || value == '_' || value == '$'
}

func containsBlockedSQL(tokens []sqlToken) bool {
	for index, tokenInfo := range tokens {
		// Quoted identifiers are not SQL syntax even when they are named after a
		// blocked keyword (for example SELECT "drop" FROM metadata). They remain
		// in tokenInfos so a quoted dangerous function call can still be detected
		// by the per-engine blocklist below.
		if tokenInfo.quoted {
			continue
		}
		token := tokenInfo.text
		switch token {
		case "insert", "update", "delete", "drop", "alter", "truncate", "create", "attach", "detach",
			"vacuum", "pragma", "grant", "revoke", "reindex", "analyze", "cluster", "refresh",
			"call", "execute", "reset", "lock", "unlock", "begin", "commit", "rollback",
			"savepoint", "release", "merge", "upsert", "outfile", "dumpfile", "install":
			return true
		case "replace", "comment", "do", "use", "set", "load":
			if index == 0 && !tokenInfo.qualified {
				return true
			}
		case "into":
			if tokenInfo.qualified {
				continue
			}
			if looksLikeSelectInto(tokens, index) {
				return true
			}
		}
	}
	return false
}

func looksLikeSelectInto(tokens []sqlToken, index int) bool {
	next, ok := nextSQLToken(tokens, index)
	if !ok || (!next.quoted && (next.text == "from" || next.text == "as")) {
		return false
	}
	if !next.quoted && (next.text == "outfile" || next.text == "dumpfile") {
		return true
	}
	for previous := index - 1; previous >= 0; previous-- {
		// A quoted identifier named after a clause (for example "from") is
		// data, not a clause boundary. Keep scanning to the actual SELECT.
		if tokens[previous].quoted {
			continue
		}
		switch tokens[previous].text {
		case "select":
			return true
		case "from", "where", "join", "on", "group", "order", "having", "limit", "offset", "union":
			return false
		}
	}
	return true
}

func nextSQLToken(tokens []sqlToken, index int) (sqlToken, bool) {
	if index+1 >= len(tokens) {
		return sqlToken{}, false
	}
	return tokens[index+1], true
}

func containsBlockedSQLForKind(analysis analysis, kind string) bool {
	if containsBlockedSQL(analysis.tokenInfos) {
		return true
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return false
	}
	extra := extraBlockedSQLTokensForKind(kind)
	if len(extra) == 0 {
		return false
	}
	for _, token := range analysis.tokenInfos {
		if _, blocked := extra[token.text]; blocked && token.called {
			return true
		}
	}
	return false
}

func extraBlockedSQLTokensForKind(kind string) map[string]struct{} {
	switch kind {
	case "postgres":
		return map[string]struct{}{
			"copy": {}, "dblink": {}, "dblink_connect": {}, "lo_export": {},
			"pg_ls_dir": {}, "pg_ls_logdir": {}, "pg_read_binary_file": {}, "pg_read_file": {},
			"pg_read_server_files": {}, "pg_rotate_logfile": {}, "pg_sleep": {}, "pg_stat_file": {},
			"pg_write_file": {}, "postgres_fdw_handler": {},
		}
	case "mysql":
		return map[string]struct{}{"benchmark": {}, "load_file": {}, "sleep": {}}
	case "sqlite":
		return map[string]struct{}{"load_extension": {}}
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
