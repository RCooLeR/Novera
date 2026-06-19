package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"":               true,
		"localhost":      true,
		"LocalHost":      true,
		"127.0.0.1":      true,
		"127.0.0.5":      true,
		"::1":            true,
		"[::1]":          true,
		"db.example.com": false,
		"10.0.0.5":       false,
		"192.168.1.10":   false,
		"0.0.0.0":        false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestDSNPostgresSecureByDefault(t *testing.T) {
	// Network host with no explicit mode must require TLS (was the unsafe `prefer`).
	_, dsn, err := dsnFor(Profile{Kind: "postgres", Host: "db.example.com", Database: "app", User: "u"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "sslmode=require") {
		t.Errorf("network postgres should default to sslmode=require, got %q", dsn)
	}

	// Loopback keeps the lenient `prefer` so local servers without TLS still work.
	_, dsn, err = dsnFor(Profile{Kind: "postgres", Host: "localhost", Database: "app", User: "u"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "sslmode=prefer") {
		t.Errorf("loopback postgres should default to sslmode=prefer, got %q", dsn)
	}

	// An explicit mode always wins, even on a network host.
	_, dsn, err = dsnFor(Profile{Kind: "postgres", Host: "db.example.com", Database: "app", User: "u", SSLMode: "disable"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "sslmode=disable") {
		t.Errorf("explicit sslmode must be honored, got %q", dsn)
	}
}

func TestDSNMySQLSecureByDefault(t *testing.T) {
	// Network host with no explicit mode must encrypt in transit.
	_, dsn, err := dsnFor(Profile{Kind: "mysql", Host: "db.example.com", Database: "app", User: "u"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "tls=skip-verify") {
		t.Errorf("network mysql should default to TLS, got %q", dsn)
	}

	// Loopback stays plaintext-friendly (no tls param forces a default that would
	// break local servers without TLS).
	_, dsn, err = dsnFor(Profile{Kind: "mysql", Host: "127.0.0.1", Database: "app", User: "u"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if contains(dsn, "tls=") {
		t.Errorf("loopback mysql should not force TLS, got %q", dsn)
	}

	// Explicit verify mode maps to full verification.
	_, dsn, err = dsnFor(Profile{Kind: "mysql", Host: "db.example.com", Database: "app", User: "u", SSLMode: "verify-full"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(dsn, "tls=true") {
		t.Errorf("verify-full mysql should map to tls=true, got %q", dsn)
	}
}

func TestSQLiteQueryIsReadOnlyAndLimited(t *testing.T) {
	path := seedSQLite(t)
	s := &Service{profiles: []Profile{{ID: "local", Name: "Local", Kind: "sqlite", File: path}}}

	got, err := s.Query("local", "SELECT id, name FROM users ORDER BY id", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.RowCount != 1 || !got.Truncated {
		t.Fatalf("RowCount=%d Truncated=%v, want 1/true", got.RowCount, got.Truncated)
	}
	if len(got.Rows) != 1 || got.Rows[0][1] != "Ada" {
		t.Fatalf("unexpected rows: %+v", got.Rows)
	}

	if _, err := s.Query("local", "DELETE FROM users", 10); err == nil {
		t.Fatal("DELETE should be rejected by read-only query guard")
	}
	if _, err := s.Query("local", "SELECT id FROM users; DROP TABLE users;", 10); err == nil {
		t.Fatal("multi-statement query should be rejected")
	}

	after, err := s.Query("local", "SELECT count(*) FROM users", 10)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rows[0][0] != "2" {
		t.Fatalf("table changed after rejected writes, count=%q", after.Rows[0][0])
	}
}

func TestSQLiteListColumnsRejectsUnsafeTableName(t *testing.T) {
	path := seedSQLite(t)
	s := &Service{profiles: []Profile{{ID: "local", Name: "Local", Kind: "sqlite", File: path}}}
	if _, err := s.ListColumns("local", `users"; DROP TABLE users; --`); err == nil {
		t.Fatal("unsafe sqlite table identifier should be rejected")
	}
	cols, err := s.ListColumns("local", "users")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0].Name != "id" || cols[1].Name != "name" {
		t.Fatalf("columns = %+v, want id/name", cols)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func seedSQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO users (id, name) VALUES (1, 'Ada'), (2, 'Bob');`); err != nil {
		t.Fatal(err)
	}
	return path
}
