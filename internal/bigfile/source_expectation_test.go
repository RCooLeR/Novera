package bigfile

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/document"
)

func TestDocumentSourceExpectationDetectsSameSizeContentRewrite(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	original := []byte("SELECT 'old.test';\n")
	changed := []byte("SELECT 'new.test';\n")
	if len(original) != len(changed) {
		t.Fatal("test fixture sizes differ")
	}
	if err := os.WriteFile(sourcePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	expected, err := captureDocumentSourceExpectation(context.Background(), doc, sourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	// Restore the coarse metadata signals. Full revalidation must still detect
	// the byte change.
	if err := os.Chtimes(sourcePath, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := expected.validateContext(context.Background(), doc, nil); !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("validation error = %v, want ErrOutputSourceChanged", err)
	}
}

func TestWriteSafeOutputSourceMutationPreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	source := []byte("SELECT 'source';\n")
	existing := []byte("existing output")
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

	_, err = writeSafeOutput(doc, sourcePath, outputPath, func(output io.Writer) error {
		if _, writeErr := output.Write([]byte("new complete output")); writeErr != nil {
			return writeErr
		}
		return os.WriteFile(sourcePath, []byte("SELECT 'mutated';\n"), 0o600)
	})
	if !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("error = %v, want ErrOutputSourceChanged", err)
	}
	assertBigFileBytes(t, outputPath, existing)
	temps, globErr := filepath.Glob(filepath.Join(dir, ".novera-bigfile-*.tmp"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary outputs remain: %v", temps)
	}
}
