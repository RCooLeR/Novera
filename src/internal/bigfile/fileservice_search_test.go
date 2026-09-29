package bigfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/encodingx"
	searchpkg "novera/internal/bigfile/search"
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

func findNextOwned(t *testing.T, service *FileService, fileID, query string, fromByte int64, regex, caseSensitive, wholeWord bool) (SearchHit, error) {
	t.Helper()
	requestID, err := service.BeginSearchRequest(fileID)
	if err != nil {
		return SearchHit{}, err
	}
	return service.FindNextRequest(requestID, query, fromByte, regex, caseSensitive, wholeWord)
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

	hit, err := findNextOwned(t, svc, meta.FileID, "users", 0, false, true, false)
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
	hit, err := findNextOwned(t, svc, meta.FileID, "w.rld", 0, true, true, false)
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
	hit, err := findNextOwned(t, svc, meta.FileID, "orders", 0, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Found || hit.Unsupported {
		t.Fatalf("expected plain UTF-8 match, got %+v", hit)
	}
}

func TestSearchRejectsOversizedQueryBeforeFileLookup(t *testing.T) {
	query := strings.Repeat("x", searchpkg.MaxPlainPatternBytes+1)
	path := writeTempFile(t, "oversized-query.txt", []byte("content"))
	service := NewFileService()
	meta, openErr := service.OpenFile(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer service.CloseFile(meta.FileID)
	_, err := findNextOwned(t, service, meta.FileID, query, 0, false, true, false)
	if !errors.Is(err, searchpkg.ErrResourceLimit) {
		t.Fatalf("error = %v, want search resource limit", err)
	}
}

func TestFindNextRejectsUnsupportedPlainSearchSemantics(t *testing.T) {
	utf8Path := writeTempFile(t, "unicode.txt", []byte("CAFÉ café"))
	service := NewFileService()
	utf8Meta, err := service.OpenFile(utf8Path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(utf8Meta.FileID)
	hit, err := findNextOwned(t, service, utf8Meta.FileID, "é", 0, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Unsupported {
		t.Fatalf("non-ASCII case-insensitive search = %+v, want unsupported", hit)
	}

	utf16, err := encodingx.EncodeString("UTF-16LE", "hello world")
	if err != nil {
		t.Fatal(err)
	}
	utf16Path := writeTempFile(t, "whole-word.txt", append(encodingx.BOMBytes("UTF-16LE"), utf16...))
	utf16Meta, err := service.OpenFile(utf16Path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(utf16Meta.FileID)
	hit, err = findNextOwned(t, service, utf16Meta.FileID, "world", 0, false, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Unsupported {
		t.Fatalf("UTF-16 whole-word search = %+v, want unsupported", hit)
	}
}

func TestFindNextUTF16RejectsUnalignedByteFalsePositive(t *testing.T) {
	// After the two-byte BOM, 41 00 first appears at odd absolute offset 3,
	// spanning two UTF-16 code units. The real U+0041 is at offset 6.
	data := append(encodingx.BOMBytes("UTF-16LE"), []byte{0x00, 0x41, 0x00, 0x42, 0x41, 0x00}...)
	path := writeTempFile(t, "alignment.txt", data)
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	hit, err := findNextOwned(t, service, meta.FileID, "A", 0, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Found || hit.Offset != 6 {
		t.Fatalf("hit = %+v, want aligned offset 6", hit)
	}
}
