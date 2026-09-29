package highlight

// TokenKind is a lightweight lexical token type.
type TokenKind string

const (
	TokenText        TokenKind = "text"
	TokenKeyword     TokenKind = "keyword"
	TokenString      TokenKind = "string"
	TokenComment     TokenKind = "comment"
	TokenNumber      TokenKind = "number"
	TokenIdentifier  TokenKind = "identifier"
	TokenOperator    TokenKind = "operator"
	TokenType        TokenKind = "type"
	TokenFunction    TokenKind = "function"
	TokenMember      TokenKind = "member"
	TokenTag         TokenKind = "tag"
	TokenAttribute   TokenKind = "attribute"
	TokenPunctuation TokenKind = "punctuation"
)

// Token is a visible-range highlight token.
type Token struct {
	Start int
	End   int
	Kind  TokenKind
}
