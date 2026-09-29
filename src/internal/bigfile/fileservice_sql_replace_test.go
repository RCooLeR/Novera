package bigfile

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installSQLSaveDialog(t *testing.T, dialog func(string, string) (string, error)) {
	t.Helper()
	previous := sqlSaveDialog
	sqlSaveDialog = dialog
	t.Cleanup(func() { sqlSaveDialog = previous })
}

func assertBigFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s mismatch\n got: %q\nwant: %q", path, got, want)
	}
}

func assertNoBigFileOutput(t *testing.T, outputPath string) {
	t.Helper()
	if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output became visible: %v", err)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(outputPath), ".novera-bigfile-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary outputs remain: %v", temps)
	}
}

func TestSqlReplaceViaDialogRecountsWordPressSerializedStringLengths(t *testing.T) {
	dir := t.TempDir()
	// `.dump` is one of the canonical SQL plugin patterns. Keep this regression
	// on that spelling so opening an ordinary database dump cannot silently
	// fall back to the old "serialization safety" blanket disablement.
	sourcePath := filepath.Join(dir, "wordpress.dump")
	outputPath := filepath.Join(dir, "replaced.sql")
	source := []byte("INSERT INTO `wp_options` VALUES (1,'siteurl','a:2:{s:3:\"url\";s:15:\"http://old.test\";s:5:\"emoji\";s:4:\"\xf0\x9f\x98\x80\";}');\n")
	want := []byte("INSERT INTO `wp_options` VALUES (1,'siteurl','a:2:{s:3:\"url\";s:18:\"http://new.example\";s:5:\"emoji\";s:4:\"\xf0\x9f\x98\x80\";}');\n")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}

	service := NewFileService()
	meta, err := service.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Detected != "SQL" {
		t.Fatalf("detected type = %q, want SQL for .dump", meta.Detected)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	installSQLSaveDialog(t, func(string, string) (string, error) { return outputPath, nil })

	result, err := service.SqlReplaceViaDialog(meta.FileID, "old.test", "new.example", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.OutputPath != outputPath || result.RecordsWritten != 1 ||
		!strings.Contains(result.Note, "serialized byte lengths recalculated") {
		t.Fatalf("result = %#v", result)
	}
	assertBigFileBytes(t, sourcePath, source)
	assertBigFileBytes(t, outputPath, want)
}

func TestSqlReplaceViaDialogLeavesCommentsAndIdentifiersUntouched(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "comments.sql")
	outputPath := filepath.Join(dir, "replaced.sql")
	source := []byte("-- 'old.test' comment\n/* 'old.test' comment */\nCREATE TABLE `old.test` (value text);\nINSERT INTO `old.test` VALUES ('old.test');\n")
	want := []byte("-- 'old.test' comment\n/* 'old.test' comment */\nCREATE TABLE `old.test` (value text);\nINSERT INTO `old.test` VALUES ('new.example');\n")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}

	service := NewFileService()
	meta, err := service.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	installSQLSaveDialog(t, func(string, string) (string, error) { return outputPath, nil })

	result, err := service.SqlReplaceViaDialog(meta.FileID, "old.test", "new.example", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordsWritten != 1 {
		t.Fatalf("result = %#v", result)
	}
	assertBigFileBytes(t, sourcePath, source)
	assertBigFileBytes(t, outputPath, want)
}

func TestSqlReplaceViaDialogRejectsRegexBeforeFileLookupOrDialog(t *testing.T) {
	dialogCalls := 0
	installSQLSaveDialog(t, func(string, string) (string, error) {
		dialogCalls++
		return "unexpected.sql", nil
	})

	_, err := NewFileService().SqlReplaceViaDialog("missing", "old[.]test", "new.example", true, false, false)
	if !errors.Is(err, ErrSQLRegexReplaceUnsupported) {
		t.Fatalf("error = %v, want ErrSQLRegexReplaceUnsupported", err)
	}
	if dialogCalls != 0 {
		t.Fatalf("dialog calls = %d, want 0", dialogCalls)
	}
}

func TestSqlReplaceViaDialogRejectsNonUTF8BeforeDialog(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "utf16.sql")
	utf16 := make([]byte, 2+2*len("SELECT 'old.test';\n"))
	utf16[0], utf16[1] = 0xff, 0xfe
	for index, char := range []byte("SELECT 'old.test';\n") {
		binary.LittleEndian.PutUint16(utf16[2+2*index:], uint16(char))
	}
	if err := os.WriteFile(sourcePath, utf16, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	dialogCalls := 0
	installSQLSaveDialog(t, func(string, string) (string, error) {
		dialogCalls++
		return filepath.Join(dir, "unexpected.sql"), nil
	})

	_, err = service.SqlReplaceViaDialog(meta.FileID, "old.test", "new.example", false, false, false)
	if !errors.Is(err, ErrSQLReplaceEncodingUnsupported) {
		t.Fatalf("error = %v, want ErrSQLReplaceEncodingUnsupported", err)
	}
	if dialogCalls != 0 {
		t.Fatalf("dialog calls = %d, want 0", dialogCalls)
	}
}

func TestSqlReplaceViaDialogRejectsMalformedSerializationWithoutPublishing(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "malformed.sql")
	outputPath := filepath.Join(dir, "existing.sql")
	source := []byte("INSERT INTO wp_options VALUES ('s:99:\"http://old.test\";');\n")
	existing := []byte("keep existing destination")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	installSQLSaveDialog(t, func(string, string) (string, error) { return outputPath, nil })

	_, err = service.SqlReplaceViaDialog(meta.FileID, "old.test", "new.example", false, false, false)
	if err == nil || !strings.Contains(err.Error(), "unsupported or malformed PHP/WordPress serialized data") {
		t.Fatalf("error = %v, want malformed serialization rejection", err)
	}
	assertBigFileBytes(t, sourcePath, source)
	assertBigFileBytes(t, outputPath, existing)
	temps, globErr := filepath.Glob(filepath.Join(dir, ".novera-bigfile-*.tmp"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary outputs remain: %v", temps)
	}
}

func TestSqlReplaceViaDialogRejectsRefreshedGenerationAfterDialog(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "refresh.sql")
	outputPath := filepath.Join(dir, "replaced.sql")
	source := []byte("INSERT INTO t VALUES ('old.test');\n")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	installSQLSaveDialog(t, func(string, string) (string, error) {
		if _, refreshErr := service.RefreshFile(meta.FileID); refreshErr != nil {
			return "", refreshErr
		}
		return outputPath, nil
	})

	_, err = service.SqlReplaceViaDialog(meta.FileID, "old.test", "new.example", false, false, false)
	if !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("error = %v, want ErrOutputSourceChanged", err)
	}
	assertBigFileBytes(t, sourcePath, source)
	assertNoBigFileOutput(t, outputPath)
}

func TestSqlCleanupPresetsFailBeforeFileLookup(t *testing.T) {
	service := NewFileService()
	if presets := service.SqlListPresets(); len(presets) != 0 {
		t.Fatalf("presets = %v, want none", presets)
	}
	_, err := service.SqlApplyPresetViaDialog("missing", "remove-definer", "", "", "", "")
	if !errors.Is(err, ErrSQLCleanupPresetsDisabled) {
		t.Fatalf("error = %v, want ErrSQLCleanupPresetsDisabled", err)
	}
}
