package manualedit

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyFileEditCompletedManifestFailurePreservesSourceAndOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "edited.txt")
	source := "alpha bravo charlie"
	if err := os.WriteFile(srcPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	manifestErr := errors.New("completed manifest write failed")
	restore := failManualEditManifestOpenOnCall(t, 2, manifestErr)
	defer restore()

	summary, err := ApplyFileEdit(context.Background(), srcPath, outPath, Edit{
		Start: 6,
		End:   11,
		Text:  []byte("delta"),
	}, FileOptions{})
	if !errors.Is(err, manifestErr) {
		t.Fatalf("err = %v, want completed manifest failure", err)
	}
	assertFileContent(t, srcPath, source)
	assertFileContent(t, outPath, "alpha delta charlie")
	if _, statErr := os.Stat(summary.TempPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temp stat err = %v, want temp promoted to output", statErr)
	}

	manifest := readManualEditManifest(t, summary.ManifestPath)
	if manifest.Status != "failed" || !strings.Contains(manifest.Error, manifestErr.Error()) {
		t.Fatalf("manifest status/error = %q/%q, want failed completed-manifest evidence", manifest.Status, manifest.Error)
	}
}

func failManualEditManifestOpenOnCall(t *testing.T, failCall int, err error) func() {
	t.Helper()
	original := openManifestOut
	calls := 0
	openManifestOut = func(path string, exclusive bool) (io.WriteCloser, error) {
		calls++
		if calls == failCall {
			return nil, err
		}
		return original(path, exclusive)
	}
	return func() {
		openManifestOut = original
	}
}
