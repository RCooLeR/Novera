package exportx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/document"
)

func TestExportByteRangeManifestPublishFailureDeletesOutputAndPreservesSource(t *testing.T) {
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
	if _, statErr := os.Stat(outPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output stat err = %v, want cleanup after manifest failure", statErr)
	}
	assertFileBytes(t, srcPath, source)
}

func TestExportByteRangeManifestPublishFailureReportsOutputCleanupFailure(t *testing.T) {
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

	originalRemoveFile := removeFile
	removeFile = func(path string) error {
		if path == outPath {
			return errors.New("cleanup denied")
		}
		return originalRemoveFile(path)
	}
	defer func() {
		removeFile = originalRemoveFile
	}()

	_, err = ExportByteRange(context.Background(), doc, srcPath, outPath, 6, 12, Options{
		WriteManifest: true,
		ManifestPath:  manifestPath,
	})
	if err == nil {
		t.Fatal("expected manifest and cleanup failure")
	}
	if !strings.Contains(err.Error(), "cleanup denied") || !strings.Contains(err.Error(), "remove export output") {
		t.Fatalf("err = %v, want manifest failure joined with cleanup context", err)
	}
	assertFileBytes(t, srcPath, source)
	if got, readErr := os.ReadFile(outPath); readErr != nil || string(got) != "bravo\n" {
		t.Fatalf("uncleaned output = %q, %v; want written range after simulated cleanup failure", got, readErr)
	}
}

func TestSplitBySizeManifestPublishFailureDeletesPartsAndPreservesSource(t *testing.T) {
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

	_, err = SplitBySize(context.Background(), doc, srcPath, basePath, 5, SplitOptions{
		ComputeSHA256: true,
		ManifestPath:  manifestPath,
	})
	if err == nil {
		t.Fatal("expected manifest publish failure")
	}
	if matches, matchErr := filepath.Glob(filepath.Join(dir, "split.part*.txt")); matchErr != nil {
		t.Fatal(matchErr)
	} else if len(matches) != 0 {
		t.Fatalf("expected split part cleanup after manifest failure, found %v", matches)
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
