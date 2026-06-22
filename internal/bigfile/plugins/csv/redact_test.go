package csv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskValue(t *testing.T) {
	cases := []struct {
		mode RedactMode
		in   string
		want string
	}{
		{RedactNull, "secret", ""},
		{RedactFixed, "secret", "REDACTED"},
		{RedactEmail, "alice@example.com", "a***@example.com"},
		{RedactEmail, "noatsign", "n***"},
		{RedactEmail, "", ""},
	}
	for _, c := range cases {
		if got := maskValue(c.in, c.mode, "REDACTED"); got != c.want {
			t.Fatalf("maskValue(%q,%s)=%q want %q", c.in, c.mode, got, c.want)
		}
	}
	// hash is stable and short
	h1 := maskValue("alice", RedactHash, "")
	h2 := maskValue("alice", RedactHash, "")
	if h1 != h2 || len(h1) != 8 {
		t.Fatalf("hash not stable/short: %q %q", h1, h2)
	}
	if maskValue("bob", RedactHash, "") == h1 {
		t.Fatal("hash collision for different inputs")
	}
}

func TestRedactColumnsFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.csv")
	dst := filepath.Join(dir, "out.csv")
	if err := os.WriteFile(src, []byte("id,name,email\n1,Alice,alice@x.com\n2,Bob,bob@y.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := RedactColumnsFile(context.Background(), src, dst, RedactOptions{
		Delimiter: ',',
		HasHeader: true,
		Columns:   map[int]RedactMode{1: RedactFixed, 2: RedactEmail},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.RecordsWritten != 3 || sum.CellsMasked != 4 {
		t.Fatalf("summary = %+v", sum)
	}
	got, _ := os.ReadFile(dst)
	want := "id,name,email\n1,REDACTED,a***@x.com\n2,REDACTED,b***@y.com\n"
	if string(got) != want {
		t.Fatalf("output = %q\nwant %q", string(got), want)
	}
	if strings.Contains(string(got), "Alice") || strings.Contains(string(got), "bob@y.com") {
		t.Fatal("PII leaked into redacted output")
	}
}
