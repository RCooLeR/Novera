package bigfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/document"
)

func TestSqlExtractTableRejectsRefreshedGenerationAfterDialog(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "dump.sql")
	outputPath := filepath.Join(dir, "users.sql")
	source := []byte("CREATE TABLE users (id int);\nINSERT INTO users VALUES (1);\n")
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
	if _, err := service.SqlAnalyze(meta.FileID); err != nil {
		t.Fatal(err)
	}
	installSQLSaveDialog(t, func(string, string) (string, error) {
		if _, refreshErr := service.RefreshFile(meta.FileID); refreshErr != nil {
			return "", refreshErr
		}
		return outputPath, nil
	})

	_, err = service.SqlExtractTableViaDialog(meta.FileID, "users")
	if !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("error = %v, want ErrOutputSourceChanged", err)
	}
	assertBigFileBytes(t, sourcePath, source)
	assertBigFileBytes(t, outputPath, existing)
}

func TestExportRangesValidatedPreservesDestinationOnValidationFailure(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "dump.sql")
	outputPath := filepath.Join(dir, "users.sql")
	source := []byte("CREATE TABLE users (id int);\n")
	existing := []byte("keep existing destination")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	validationErr := errors.New("source snapshot mismatch")
	_, err = exportRangesValidated(
		t.Context(),
		doc,
		sourcePath,
		outputPath,
		[][2]int64{{0, int64(len(source))}},
		nil,
		func(context.Context) error { return validationErr },
	)
	if !errors.Is(err, validationErr) {
		t.Fatalf("error = %v, want validation failure", err)
	}
	assertBigFileBytes(t, outputPath, existing)
	assertBigFileBytes(t, sourcePath, source)
}
