package db

import "testing"

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"":             true,
		"localhost":    true,
		"LocalHost":    true,
		"127.0.0.1":    true,
		"127.0.0.5":    true,
		"::1":          true,
		"[::1]":        true,
		"db.example.com": false,
		"10.0.0.5":     false,
		"192.168.1.10": false,
		"0.0.0.0":      false,
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
