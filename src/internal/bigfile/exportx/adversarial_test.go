package exportx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/fileio"
)

func TestExportByteRangeManifestPublishFailurePreservesCompleteOutputAndSource(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	manifestPath := filepath.Join(dir, "missing-dir", "manifest.json")
	source := []byte("alpha\nbravo\ncharlie\n")
	if err := os.WriteFile(srcPath, source, 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	_, err = ExportByteRange(context.Background(), doc, srcPath, outPath, 6, 12, Options{
		ComputeSHA256: true,
		ManifestPath:  manifestPath,
	})
	if err == nil {
		t.Fatal("expected manifest publish failure")
	}
	var publicationErr *fileio.PublicationError
	if !errors.As(err, &publicationErr) || !publicationErr.Durable {
		t.Fatalf("error = %v, want durable PublicationError", err)
	}
	if got, readErr := os.ReadFile(outPath); readErr != nil || string(got) != "bravo\n" {
		t.Fatalf("published output = %q, %v", got, readErr)
	}
	assertFileBytes(t, srcPath, source)
}

func TestSplitBySizeManifestPublishFailurePreservesCompletePartsAndSource(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	basePath := filepath.Join(dir, "split.txt")
	manifestPath := filepath.Join(dir, "missing-dir", "manifest.json")
	source := []byte("abcdefghijkl")
	if err := os.WriteFile(srcPath, source, 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	summary, err := SplitBySize(context.Background(), doc, srcPath, basePath, 5, SplitOptions{
		ComputeSHA256: true,
		ManifestPath:  manifestPath,
	})
	if err == nil {
		t.Fatal("expected manifest publish failure")
	}
	var incomplete *SplitIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want SplitIncompleteError", err)
	}
	if len(incomplete.Outputs) != 3 || len(summary.Outputs) != 3 {
		t.Fatalf("preserved outputs = %v, summary outputs = %v; want three complete parts", incomplete.Outputs, summary.Outputs)
	}
	for _, path := range incomplete.Outputs {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("preserved part %q: %v", path, statErr)
		}
	}
	assertFileBytes(t, srcPath, source)
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s changed to %q, want %q", path, got, want)
	}
}
