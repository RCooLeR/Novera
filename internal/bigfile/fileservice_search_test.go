package bigfile

import (
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/encodingx"
)

// writeTempFile writes data to a temp file and returns its path.
func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A UTF-16LE file used to return a false "no matches" because the UTF-8 query
// bytes were compared against the raw UTF-16 bytes. The query must now be
// encoded into the document's encoding before searching.
func TestFindNextMatchesInUTF16File(t *testing.T) {
	body := "alpha beta CREATE TABLE users gamma"
	utf16, err := encodingx.EncodeString("UTF-16LE", body)
	if err != nil {
		t.Fatal(err)
	}
	// Prepend a UTF-16LE BOM so detection classifies it as UTF-16LE.
	data := append(encodingx.BOMBytes("UTF-16LE"), utf16...)
	path := writeTempFile(t, "u16.sql", data)

	svc := NewFileService()
	meta, err := svc.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.CloseFile(meta.FileID)
	if meta.Encoding != "UTF-16LE" {
		t.Fatalf("encoding = %q, want UTF-16LE", meta.Encoding)
	}

	hit, err := svc.FindNext(meta.FileID, "users", 0, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Unsupported {
		t.Fatalf("plain search should be supported in UTF-16: %+v", hit)
	}
	if !hit.Found {
		t.Fatalf("expected to find 'users' in the UTF-16 file, got %+v", hit)
	}
}

// Regex over a non-UTF-8 file is reported as unsupported rather than silently
// returning no matches.
func TestFindNextRegexUnsupportedOnUTF16(t *testing.T) {
	utf16, err := encodingx.EncodeString("UTF-16LE", "hello world")
	if err != nil {
		t.Fatal(err)
	}
	data := append(encodingx.BOMBytes("UTF-16LE"), utf16...)
	path := writeTempFile(t, "u16-re.txt", data)

	svc := NewFileService()
	meta, err := svc.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.CloseFile(meta.FileID)
	hit, err := svc.FindNext(meta.FileID, "w.rld", 0, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Unsupported {
		t.Fatalf("regex search on UTF-16 should be reported unsupported, got %+v", hit)
	}
}

// Plain search in a UTF-8 file still works (regression guard for the common path).
func TestFindNextPlainUTF8StillWorks(t *testing.T) {
	path := writeTempFile(t, "u8.sql", []byte("one two CREATE TABLE orders three"))
	svc := NewFileService()
	meta, err := svc.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.CloseFile(meta.FileID)
	hit, err := svc.FindNext(meta.FileID, "orders", 0, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Found || hit.Unsupported {
		t.Fatalf("expected plain UTF-8 match, got %+v", hit)
	}
}
