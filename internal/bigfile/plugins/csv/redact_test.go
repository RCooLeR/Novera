package csv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskValue(t *testing.T) {
	key := bytes.Repeat([]byte{0x41}, PseudonymKeyBytes)
	cases := []struct {
		mode RedactMode
		in   string
		want string
	}{
		{RedactNull, "secret", ""},
		{RedactFixed, "secret", "REDACTED"},
		{RedactEmail, "alice@example.com", "a***@example.com"},
		{RedactEmail, "noatsign", "n***"},
		{RedactEmail, "é@example.com", "é***@example.com"},
		{RedactEmail, "Жанна", "Ж***"},
		{RedactEmail, "", ""},
	}
	for _, c := range cases {
		got, err := maskValue(c.in, c.mode, "REDACTED", nil)
		if err != nil {
			t.Fatalf("maskValue(%q,%s) error = %v", c.in, c.mode, err)
		}
		if got != c.want {
			t.Fatalf("maskValue(%q,%s)=%q want %q", c.in, c.mode, got, c.want)
		}
	}
	// A keyed pseudonym is stable within one operation and uses 128 output bits.
	h1, err := maskValue("alice", RedactHash, "", key)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := maskValue("alice", RedactHash, "", key)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 || len(h1) != 2*pseudonymOutputBytes {
		t.Fatalf("pseudonym not stable or collision-resistant length: %q %q", h1, h2)
	}
	h3, err := maskValue("bob", RedactHash, "", key)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h1 {
		t.Fatal("pseudonym collision for different inputs")
	}
	other, err := maskValue("alice", RedactHash, "", bytes.Repeat([]byte{0x42}, PseudonymKeyBytes))
	if err != nil {
		t.Fatal(err)
	}
	if other == h1 {
		t.Fatal("different operation keys produced linkable pseudonyms")
	}
	if _, err := maskValue("secret", RedactHash, "", nil); err == nil {
		t.Fatal("hash mode without a key did not fail closed")
	}
	if _, err := maskValue(string([]byte{0xff}), RedactEmail, "", nil); err == nil {
		t.Fatal("invalid UTF-8 email mask error = nil")
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

func TestRedactColumnsFileKeyedPseudonymsAreOperationLocal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(src, []byte("id,value\n1,alice\n2,alice\n3,bob\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys := [][]byte{
		bytes.Repeat([]byte{0x11}, PseudonymKeyBytes),
		bytes.Repeat([]byte{0x22}, PseudonymKeyBytes),
	}
	outputs := make([][]string, 0, len(keys))
	for i, key := range keys {
		dst := filepath.Join(dir, "out-"+string(rune('a'+i))+".csv")
		if _, err := RedactColumnsFile(context.Background(), src, dst, RedactOptions{
			Delimiter: ',', HasHeader: true, Columns: map[int]RedactMode{1: RedactHash}, PseudonymKey: key,
		}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		outputs = append(outputs, lines)
		alice1 := strings.Split(lines[1], ",")[1]
		alice2 := strings.Split(lines[2], ",")[1]
		bob := strings.Split(lines[3], ",")[1]
		if alice1 != alice2 || alice1 == bob || len(alice1) != 2*pseudonymOutputBytes {
			t.Fatalf("operation %d pseudonyms = %#v", i, lines)
		}
	}
	if strings.Split(outputs[0][1], ",")[1] == strings.Split(outputs[1][1], ",")[1] {
		t.Fatal("different operation keys produced linkable pseudonyms")
	}
}

func TestRedactHashWithoutKeyFailsBeforeOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.csv")
	dst := filepath.Join(dir, "out.csv")
	if err := os.WriteFile(src, []byte("id,value\n1,secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RedactColumnsFile(context.Background(), src, dst, RedactOptions{
		Delimiter: ',', HasHeader: true, Columns: map[int]RedactMode{1: RedactHash},
	}); err == nil || !strings.Contains(err.Error(), "per-operation key") {
		t.Fatalf("missing-key error = %v", err)
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key created output: %v", err)
	}
}

func TestValidateRedactOptionsRejectsUnsafeConfiguration(t *testing.T) {
	t.Run("invalid mode", func(t *testing.T) {
		err := ValidateRedactOptions(RedactOptions{
			Delimiter: ',',
			Columns:   map[int]RedactMode{0: "typo"},
		})
		if err == nil || !strings.Contains(err.Error(), "invalid redaction mode") {
			t.Fatalf("error = %v, want invalid redaction mode", err)
		}
	})

	t.Run("negative index", func(t *testing.T) {
		err := ValidateRedactOptions(RedactOptions{
			Delimiter: ',',
			Columns:   map[int]RedactMode{-1: RedactFixed},
		})
		if err == nil || !strings.Contains(err.Error(), "negative") {
			t.Fatalf("error = %v, want negative index error", err)
		}
	})

	t.Run("hash without operation key", func(t *testing.T) {
		err := ValidateRedactOptions(RedactOptions{
			Delimiter: ',',
			Columns:   map[int]RedactMode{0: RedactHash},
		})
		if err == nil || !strings.Contains(err.Error(), "per-operation key") {
			t.Fatalf("error = %v, want per-operation key error", err)
		}
	})

	t.Run("mapping limit", func(t *testing.T) {
		columns := make(map[int]RedactMode, MaxTransformColumnMappings+1)
		for i := 0; i <= MaxTransformColumnMappings; i++ {
			columns[i] = RedactNull
		}
		err := ValidateRedactOptions(RedactOptions{Delimiter: ',', Columns: columns})
		if err == nil || !strings.Contains(err.Error(), "maximum") {
			t.Fatalf("error = %v, want mapping limit", err)
		}
	})

	t.Run("configuration string limit", func(t *testing.T) {
		err := ValidateRedactOptions(RedactOptions{
			Delimiter:   ',',
			Columns:     map[int]RedactMode{0: RedactFixed},
			Replacement: strings.Repeat("x", MaxTransformConfigStringBytes),
		})
		if err == nil || !strings.Contains(err.Error(), "aggregate limit") {
			t.Fatalf("error = %v, want configuration string limit", err)
		}
	})

	t.Run("combined replacement expansion", func(t *testing.T) {
		columns := make(map[int]RedactMode, MaxTransformColumnMappings)
		for i := 0; i < MaxTransformColumnMappings; i++ {
			columns[i] = RedactFixed
		}
		replacementBytes := MaxTransformConfigStringBytes - len(string(RedactFixed))*len(columns)
		err := ValidateRedactOptions(RedactOptions{
			Delimiter:   ',',
			Columns:     columns,
			Replacement: strings.Repeat("x", replacementBytes),
		})
		if err == nil || !strings.Contains(err.Error(), "output-record limit") {
			t.Fatalf("error = %v, want output-record limit", err)
		}
	})
}

func TestRedactRejectsInvalidSelectionBeforePublishing(t *testing.T) {
	t.Run("invalid mode before source open", func(t *testing.T) {
		dir := t.TempDir()
		dst := filepath.Join(dir, "out.csv")
		_, err := RedactColumnsFile(context.Background(), filepath.Join(dir, "missing.csv"), dst, RedactOptions{
			Columns: map[int]RedactMode{0: "typo"},
		})
		if err == nil || !strings.Contains(err.Error(), "invalid redaction mode") {
			t.Fatalf("error = %v, want invalid redaction mode", err)
		}
		if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("destination exists after rejected configuration: %v", statErr)
		}
	})

	t.Run("column outside first record", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "in.csv")
		dst := filepath.Join(dir, "out.csv")
		if err := os.WriteFile(src, []byte("only\nvalue\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := RedactColumnsFile(context.Background(), src, dst, RedactOptions{
			Columns: map[int]RedactMode{1: RedactNull},
		})
		if err == nil || !strings.Contains(err.Error(), "outside the first record") {
			t.Fatalf("error = %v, want selected-column range error", err)
		}
		if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("destination exists after rejected selection: %v", statErr)
		}
	})
}

func TestRedactCellsMaskedCountsOnlyChangedValues(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.csv")
	dst := filepath.Join(dir, "out.csv")
	if err := os.WriteFile(src, []byte("same\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := RedactColumnsFile(context.Background(), src, dst, RedactOptions{
		Columns:     map[int]RedactMode{0: RedactFixed},
		Replacement: "same",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.CellsMasked != 0 {
		t.Fatalf("cells masked = %d, want 0 for unchanged replacement", sum.CellsMasked)
	}
}
