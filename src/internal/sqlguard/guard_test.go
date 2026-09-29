package sqlguard

import "testing"

// Every supported engine must have a non-empty dangerous-function blocklist, so
// the db layer (which validates kinds against SupportedKinds) can never accept a
// kind the guard doesn't cover.
func TestEverySupportedKindHasGuard(t *testing.T) {
	for _, kind := range SupportedKinds {
		if len(extraBlockedSQLTokensForKind(kind)) == 0 {
			t.Errorf("supported kind %q has no dangerous-function blocklist", kind)
		}
	}
}

func TestNormalizeReadOnlyAllowsSelect(t *testing.T) {
	out, err := NormalizeReadOnly("SELECT 1", Options{Kind: "sqlite", AllowWith: true})
	if err != nil {
		t.Fatalf("plain SELECT rejected: %v", err)
	}
	if out != "SELECT 1" {
		t.Fatalf("unexpected normalized output: %q", out)
	}
}

func TestNormalizeReadOnlyStripsComments(t *testing.T) {
	out, err := NormalizeReadOnly("SELECT 1 /* keep me out */ -- trailing\n", Options{Kind: "postgres", AllowWith: true})
	if err != nil {
		t.Fatalf("commented SELECT rejected: %v", err)
	}
	if want := "SELECT 1"; out != want {
		t.Fatalf("comments not stripped from executed text: got %q want %q", out, want)
	}
}

func TestNormalizeReadOnlyRejectsMultipleStatements(t *testing.T) {
	if _, err := NormalizeReadOnly("SELECT 1; SELECT 2", Options{Kind: "sqlite"}); err == nil {
		t.Fatal("multiple statements were not rejected")
	}
}

func TestNormalizeReadOnlyRejectsWrites(t *testing.T) {
	for _, q := range []string{"DELETE FROM t", "UPDATE t SET a=1", "DROP TABLE t", "INSERT INTO t VALUES (1)"} {
		if _, err := NormalizeReadOnly(q, Options{Kind: "postgres"}); err == nil {
			t.Errorf("write query was not rejected: %q", q)
		}
	}
}

// SEC-04 regression: a comment between a dangerous function name and "(" must not
// slip past the per-engine blocklist.
func TestCommentDoesNotBypassDangerousFunction(t *testing.T) {
	cases := []struct {
		kind string
		sql  string
	}{
		{"postgres", "SELECT pg_read_file('/etc/passwd')"},
		{"postgres", "SELECT pg_read_file/**/('/etc/passwd')"},
		{"postgres", "SELECT pg_sleep--c\n(10)"},
		{"postgres", "SELECT pg_sleep#c\n(10)"},
		{"mysql", "SELECT sleep/**/(10)"},
		{"sqlite", "SELECT load_extension/**/('x')"},
	}
	for _, c := range cases {
		if _, err := NormalizeReadOnly(c.sql, Options{Kind: c.kind}); err == nil {
			t.Errorf("dangerous function not blocked for %s: %q", c.kind, c.sql)
		}
	}
}

// Quoted identifiers are still identifiers. PostgreSQL accepts a quoted
// lower-case function name such as "pg_sleep"(10), so skipping quote contents
// would let dangerous functions bypass the per-engine call blocklist.
func TestQuotedIdentifierDoesNotBypassDangerousFunction(t *testing.T) {
	cases := []struct {
		kind string
		sql  string
	}{
		{"postgres", `SELECT "pg_sleep"(10)`},
		{"postgres", `SELECT public."pg_read_file"('/etc/passwd')`},
		{"mysql", "SELECT `sleep`(10)"},
		{"sqlite", "SELECT [load_extension]('x')"},
	}
	for _, c := range cases {
		if _, err := NormalizeReadOnly(c.sql, Options{Kind: c.kind}); err == nil {
			t.Errorf("quoted dangerous function not blocked for %s: %q", c.kind, c.sql)
		}
	}
}

func TestQuotedBlockedKeywordRemainsUsableAsIdentifier(t *testing.T) {
	for _, q := range []string{
		`SELECT "drop" FROM metadata`,
		"SELECT `delete` FROM metadata",
		"SELECT [update] FROM metadata",
	} {
		if _, err := NormalizeReadOnly(q, Options{Kind: "sqlite"}); err != nil {
			t.Errorf("quoted identifier was treated as SQL syntax for %q: %v", q, err)
		}
	}
}

func TestQuotedClauseIdentifiersDoNotHideSelectInto(t *testing.T) {
	queries := []string{
		`SELECT "from", value INTO copied_rows FROM source_rows`,
		`SELECT value INTO "from" FROM source_rows`,
	}
	for _, query := range queries {
		if _, err := NormalizeReadOnly(query, Options{Kind: "postgres"}); err == nil {
			t.Errorf("SELECT INTO hidden by quoted clause identifier was not blocked: %q", query)
		}
	}
}
