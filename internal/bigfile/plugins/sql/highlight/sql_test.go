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
