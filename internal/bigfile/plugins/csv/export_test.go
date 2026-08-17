package csv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/fileio"
)

func TestExportJSONL(t *testing.T) {
	src := writeTemp(t, "in.csv", "id,name\n1,alice\n2,bob\n")
	dst := filepath.Join(t.TempDir(), "out.jsonl")
	sum, err := ExportJSONLFile(context.Background(), src, dst, JSONLOptions{Delimiter: ',', HasHeader: true, NumberKeys: true})
	if err != nil {
		t.Fatalf("jsonl: %v", err)
	}
	if sum.RecordsWritten != 2 {
		t.Fatalf("written = %d, want 2", sum.RecordsWritten)
	}
	got := readAll(t, dst)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), got)
	}
	if !strings.Contains(lines[0], `"name":"alice"`) || !strings.Contains(lines[0], `"id":1`) {
		t.Fatalf("first object wrong: %q", lines[0])
	}
}

func TestExportJSONLNonFiniteAndBigInt(t *testing.T) {
	// NaN/Inf stay strings, while an integer past int64 remains an exact JSON
	// number without a lossy float64 round trip.
	src := writeTemp(t, "in.csv", "a,b,c,d\nNaN,Inf,12345678901234567890,3.5\n")
	dst := filepath.Join(t.TempDir(), "out.jsonl")
	_, err := ExportJSONLFile(context.Background(), src, dst, JSONLOptions{Delimiter: ',', HasHeader: true, NumberKeys: true})
	if err != nil {
		t.Fatalf("export aborted on NaN/Inf/bigint: %v", err)
	}
	got := readAll(t, dst)
	if !strings.Contains(got, `"a":"NaN"`) || !strings.Contains(got, `"b":"Inf"`) {
		t.Fatalf("NaN/Inf not kept as strings: %q", got)
	}
	if !strings.Contains(got, `"c":12345678901234567890`) {
		t.Fatalf("high-precision integer token not preserved exactly: %q", got)
	}
	if !strings.Contains(got, `"d":3.5`) {
		t.Fatalf("genuine float not emitted as number: %q", got)
	}
}

func TestExportJSONLPreservesExactNumericTokensAndIdentifiers(t *testing.T) {
	src := writeTemp(t, "in.csv", "plus,negzero,negid,uid,big,decimal,exp,nan,space\n+007,-0,-007,007,12345678901234567890,0.12345678901234567890,1.2300e+99,NaN,\" 42 \"\n")
	dst := filepath.Join(t.TempDir(), "out.jsonl")
	if _, err := ExportJSONLFile(context.Background(), src, dst, JSONLOptions{Delimiter: ',', HasHeader: true, NumberKeys: true}); err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(readAll(t, dst))), &object); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"plus": `"+007"`, "negzero": `-0`, "negid": `"-007"`, "uid": `"007"`,
		"big": `12345678901234567890`, "decimal": `0.12345678901234567890`,
		"exp": `1.2300e+99`, "nan": `"NaN"`, "space": `" 42 "`,
	}
	for key, expected := range want {
		if got := string(object[key]); got != expected {
			t.Errorf("%s = %s, want %s", key, got, expected)
		}
	}
}

func TestExportJSONLRejectsDuplicateDerivedKeysBeforeOutput(t *testing.T) {
	for _, input := range []string{
		"id,id\nfirst,second\n",
		"col2,\nfirst,second\n",
	} {
		dir := t.TempDir()
		src := writeTemp(t, "in.csv", input)
		dst := filepath.Join(dir, "out.jsonl")
		if _, err := ExportJSONLFile(context.Background(), src, dst, JSONLOptions{Delimiter: ',', HasHeader: true}); err == nil || !strings.Contains(err.Error(), "derived by both CSV columns") {
			t.Fatalf("duplicate-key error = %v", err)
		}
		if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("duplicate keys created output: %v", err)
		}
		if _, err := os.Stat(tempOutputPath(dst)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("duplicate keys created scratch output: %v", err)
		}
	}
}

func TestExportJSONLRejectsMalformedAndRaggedRowsWithoutPublishing(t *testing.T) {
	for _, input := range []string{
		"a,b\n1\n",
		"a,b\n1,2,3\n",
		"a,b\n1,\"unterminated\n",
	} {
		dir := t.TempDir()
		src := writeTemp(t, "in.csv", input)
		dst := filepath.Join(dir, "out.jsonl")
		if _, err := ExportJSONLFile(context.Background(), src, dst, JSONLOptions{Delimiter: ',', HasHeader: true}); err == nil {
			t.Fatalf("malformed/ragged input %q unexpectedly succeeded", input)
		}
		if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("malformed/ragged input created output: %v", err)
		}
		if _, err := os.Stat(tempOutputPath(dst)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("malformed/ragged input left scratch output: %v", err)
		}
	}
}

func TestExportJSONLRejectsInvalidUTF8WithoutPublishing(t *testing.T) {
	tests := []struct {
		name      string
		input     []byte
		hasHeader bool
	}{
		{
			name:      "header keys",
			input:     []byte{0xff, ',', 0xfe, '\n', 'a', ',', 'b', '\n'},
			hasHeader: true,
		},
		{
			name:      "first data record",
			input:     []byte{'a', ',', 0xff, '\n'},
			hasHeader: false,
		},
		{
			name:      "later data record",
			input:     []byte{'a', ',', 'b', '\n', 'x', ',', 0xff, '\n'},
			hasHeader: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "in.csv")
			dst := filepath.Join(dir, "out.jsonl")
			if err := os.WriteFile(src, test.input, 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := ExportJSONLFile(context.Background(), src, dst, JSONLOptions{
				Delimiter: ',', HasHeader: test.hasHeader,
			})
			if err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
				t.Fatalf("error = %v, want invalid UTF-8 error", err)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("invalid UTF-8 created output: %v", statErr)
			}
			if _, statErr := os.Stat(tempOutputPath(dst)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("invalid UTF-8 left scratch output: %v", statErr)
			}
		})
	}
}

func TestExportJSONLNoHeader(t *testing.T) {
	src := writeTemp(t, "in.csv", "a,b\nc,d\n")
	dst := filepath.Join(t.TempDir(), "out.jsonl")
	_, err := ExportJSONLFile(context.Background(), src, dst, JSONLOptions{Delimiter: ',', HasHeader: false})
	if err != nil {
		t.Fatalf("jsonl: %v", err)
	}
	got := readAll(t, dst)
	if !strings.Contains(got, `"col1":"a"`) || !strings.Contains(got, `"col2":"b"`) {
		t.Fatalf("expected colN keys: %q", got)
	}
}

func TestUnsafeExportsFailClosedBeforeFilesystemIO(t *testing.T) {
	tests := []struct {
		name        string
		destination string
		scratch     string
		wantErr     error
		export      func(src, dst string, progress func(int64)) (bool, error)
	}{
		{
			name:        "SQLite",
			destination: "out.db",
			scratch:     "out.db.quarry-part",
			wantErr:     ErrSQLiteExportSecurePublicationUnavailable,
			export: func(src, dst string, progress func(int64)) (bool, error) {
				sum, err := ExportSQLiteFile(context.Background(), src, dst, SQLiteOptions{Progress: progress})
				return sum == (ExportSummary{}), err
			},
		},
		{
			name:        "XLSX",
			destination: "out.xlsx",
			scratch:     "out.xlsx.quarry-part.xlsx",
			wantErr:     ErrXLSXExportSecureScratchUnavailable,
			export: func(src, dst string, progress func(int64)) (bool, error) {
				sum, err := ExportXLSXFile(context.Background(), src, dst, XLSXOptions{Progress: progress})
				return sum == (XLSXSummary{}), err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "missing.csv")
			dst := filepath.Join(dir, tt.destination)
			scratch := filepath.Join(dir, tt.scratch)
			const sentinel = "existing destination"
			if err := os.WriteFile(dst, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			progressCalled := false

			zeroSummary, err := tt.export(src, dst, func(int64) {
				progressCalled = true
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if !zeroSummary {
				t.Fatal("disabled export returned a non-zero summary")
			}
			if progressCalled {
				t.Fatal("disabled export invoked progress callback")
			}
			if got := readAll(t, dst); got != sentinel {
				t.Fatalf("destination changed: got %q", got)
			}
			if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("scratch path was touched: %v", err)
			}
		})
	}
}

func TestExportPreservesExistingDestinationAndNoLeftoverTemp(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(src, []byte("id,name\n1,alice\n2,bob\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.jsonl")
	// Publication is no-clobber even if a native Save dialog previously asked
	// the user for replacement confirmation. A competing/new destination must
	// survive unchanged.
	if err := os.WriteFile(dst, []byte("STALE"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportJSONLFile(context.Background(), src, dst, JSONLOptions{Delimiter: ',', HasHeader: true}); !errors.Is(err, fileio.ErrExists) {
		t.Fatalf("error = %v, want fileio.ErrExists", err)
	}
	got := readAll(t, dst)
	if got != "STALE" {
		t.Fatalf("destination changed: %q", got)
	}
	if _, err := os.Stat(tempOutputPath(dst)); err == nil {
		t.Fatal("temp .quarry-part file was left behind")
	}
}

func TestMarkdownPreview(t *testing.T) {
	md := MarkdownPreview([]string{"id", "name"}, [][]string{{"1", "a|b"}, {"2", "c"}})
	if !strings.Contains(md, "| id | name |") {
		t.Fatalf("header missing: %q", md)
	}
	if !strings.Contains(md, "a\\|b") {
		t.Fatalf("pipe not escaped: %q", md)
	}
	if !strings.Contains(md, "| --- | --- |") {
		t.Fatalf("separator missing: %q", md)
	}
}
