package highlight

import "testing"

func TestSQLVisibleRecognizesCoreTokenKinds(t *testing.T) {
	text := "CREATE TABLE `users` (id INT, name VARCHAR(255)); -- comment"
	tokens := SQLVisible(text)

	assertHasTokenKind(t, tokens, text, "CREATE", TokenKeyword)
	assertHasTokenKind(t, tokens, text, "TABLE", TokenKeyword)
	assertHasTokenKind(t, tokens, text, "`users`", TokenIdentifier)
	assertHasTokenKind(t, tokens, text, "255", TokenNumber)
	assertHasTokenKind(t, tokens, text, "-- comment", TokenComment)
	assertHasTokenKind(t, tokens, text, "(", TokenOperator)
}

func TestSQLVisibleRecognizesStringsAndBlockComments(t *testing.T) {
	text := "INSERT INTO logs VALUES ('it''s fine', \"double\", /* note */ 42);"
	tokens := SQLVisible(text)

	assertHasTokenKind(t, tokens, text, "INSERT", TokenKeyword)
	assertHasTokenKind(t, tokens, text, "'it''s fine'", TokenString)
	assertHasTokenKind(t, tokens, text, "\"double\"", TokenString)
	assertHasTokenKind(t, tokens, text, "/* note */", TokenComment)
	assertHasTokenKind(t, tokens, text, "42", TokenNumber)
}

func TestSQLVisibleLeavesNonKeywordsAsText(t *testing.T) {
	text := "custom_table custom_column"
	tokens := SQLVisible(text)
	assertHasTokenKind(t, tokens, text, text, TokenText)
}

func TestSQLVisibleLineCommentsStopAtEveryLineEnding(t *testing.T) {
	tests := []struct {
		name      string
		separator string
		comment   string
	}{
		{name: "LF dash", separator: "\n", comment: "-- note"},
		{name: "CR dash", separator: "\r", comment: "-- note"},
		{name: "CRLF dash", separator: "\r\n", comment: "-- note"},
		{name: "LF hash", separator: "\n", comment: "# note"},
		{name: "control after dash", separator: "\r", comment: "--"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := tt.comment + tt.separator + "SELECT 1"
			tokens := SQLVisible(text)
			assertHasTokenKind(t, tokens, text, tt.comment, TokenComment)
			assertHasTokenKind(t, tokens, text, "SELECT", TokenKeyword)
			for _, token := range tokens {
				if token.Kind == TokenComment && token.End > len(tt.comment) {
					t.Fatalf("line comment crossed terminator: %+v in %q", token, text)
				}
			}
		})
	}
}

func TestSQLVisibleMySQLDashCommentRequiresWhitespace(t *testing.T) {
	text := "SELECT 4--2, 5--x;\nSELECT 1 -- comment\nSELECT 2"
	tokens := SQLVisibleDialect(text, DialectMySQL)
	assertHasTokenKind(t, tokens, text, "-- comment", TokenComment)
	if hasTokenTextAndKind(tokens, text, "--2", TokenComment) || hasTokenTextAndKind(tokens, text, "--x;", TokenComment) {
		t.Fatalf("MySQL subtraction-like dashes became comments: %#v", tokens)
	}

	ansi := "SELECT 4--2\nSELECT 1"
	assertHasTokenKind(t, SQLVisibleDialect(ansi, DialectANSI), ansi, "--2", TokenComment)
}

func TestSQLVisibleHashCommentIsMySQLSpecific(t *testing.T) {
	text := "# note\nSELECT 1"
	assertHasTokenKind(t, SQLVisibleDialect(text, DialectMySQL), text, "# note", TokenComment)
	if hasTokenTextAndKind(SQLVisibleDialect(text, DialectANSI), text, "# note", TokenComment) {
		t.Fatal("ANSI highlighting treated # as a line comment")
	}
}

func hasTokenTextAndKind(tokens []Token, text string, wantText string, wantKind TokenKind) bool {
	for _, tok := range tokens {
		if tok.Start >= 0 && tok.End <= len(text) && tok.Start < tok.End && text[tok.Start:tok.End] == wantText && tok.Kind == wantKind {
			return true
		}
	}
	return false
}

func assertHasTokenKind(t *testing.T, tokens []Token, text string, wantText string, wantKind TokenKind) {
	t.Helper()
	for _, tok := range tokens {
		if tok.Start < 0 || tok.End > len(text) || tok.Start >= tok.End {
			t.Fatalf("invalid token bounds: %+v", tok)
		}
		if text[tok.Start:tok.End] == wantText && tok.Kind == wantKind {
			return
		}
	}
	t.Fatalf("missing token %q of kind %q in %#v", wantText, wantKind, tokens)
}
