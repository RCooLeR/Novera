package bigfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlreshape "novera/internal/bigfile/plugins/sql/reshape"
)

func TestSqlReshapeRejectsInvalidModeBeforeLookupOrDialog(t *testing.T) {
	dialogCalls := 0
	installSQLSaveDialog(t, func(string, string) (string, error) {
		dialogCalls++
		return "unexpected.sql", nil
	})

	_, err := NewFileService().SqlReshapeInsertsViaDialog("missing", "future", 100)
	if err == nil || !strings.Contains(err.Error(), "unsupported reshape mode") {
		t.Fatalf("error = %v, want unsupported mode", err)
	}
	if dialogCalls != 0 {
		t.Fatalf("dialog calls = %d, want 0", dialogCalls)
	}
}

func TestSqlReshapeRejectsUnsupportedInsertWithoutReplacingDestination(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "dump.sql")
	outputPath := filepath.Join(dir, "existing.sql")
	source := []byte("INSERT INTO t VALUES (1),(2) ON DUPLICATE KEY UPDATE v=VALUES(v);\n")
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

	_, err = service.SqlReshapeInsertsViaDialog(meta.FileID, "single", 100)
	if !errors.Is(err, sqlreshape.ErrUnsupportedInsert) {
		t.Fatalf("error = %v, want ErrUnsupportedInsert", err)
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

func TestSqlReshapeRejectsRefreshedGenerationAfterDialog(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "dump.sql")
	outputPath := filepath.Join(dir, "reshaped.sql")
	source := []byte("INSERT INTO t VALUES (1),(2);\n")
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

	_, err = service.SqlReshapeInsertsViaDialog(meta.FileID, "single", 100)
	if !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("error = %v, want ErrOutputSourceChanged", err)
	}
	assertBigFileBytes(t, sourcePath, source)
	assertNoBigFileOutput(t, outputPath)
}
