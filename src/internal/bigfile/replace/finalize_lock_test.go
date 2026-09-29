package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceRegexpFileReportsLockedOutputDuringFinalize(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("ab12 cd34"), 0o600); err != nil {
		t.Fatal(err)
	}

	lockedErr := errors.New("output is locked")
	restore := installFinalizeLock(t, outPath, lockedErr)
	defer restore()

	summary, err := ReplaceRegexpFile(context.Background(), srcPath, outPath, []byte(`([a-z]{2})([0-9]{2})`), []byte(`${2}-${1}`), FileOptions{}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
	})
	assertFinalizeLockFailure(t, outPath, summary.TempPath, summary.ManifestPath, err, lockedErr)
}

func TestReplaceBatchPlainFileReportsLockedOutputDuringFinalize(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("hello world hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	lockedErr := errors.New("output is locked")
	restore := installFinalizeLock(t, outPath, lockedErr)
	defer restore()

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte("hello"), Replace: []byte("bye"), Priority: 0},
	}
	summary, err := ReplaceBatchPlainFile(context.Background(), srcPath, outPath, rules, FileOptions{}, BatchOptions{
		ChunkSize: 8,
	})
	assertFinalizeLockFailure(t, outPath, summary.TempPath, summary.ManifestPath, err, lockedErr)
}

func TestReplaceBatchRegexpFileReportsLockedOutputDuringFinalize(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("id=41 id=42"), 0o600); err != nil {
		t.Fatal(err)
	}

	lockedErr := errors.New("output is locked")
	restore := installFinalizeLock(t, outPath, lockedErr)
	defer restore()

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte(`id=(\d{2})`), Replace: []byte(`row-$1`), Priority: 0},
	}
	summary, err := ReplaceBatchRegexpFile(context.Background(), srcPath, outPath, rules, FileOptions{}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
	})
	assertFinalizeLockFailure(t, outPath, summary.TempPath, summary.ManifestPath, err, lockedErr)
}

func TestConvertLineEndingsFileReportsLockedOutputDuringFinalize(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("a\r\nb\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lockedErr := errors.New("output is locked")
	restore := installFinalizeLock(t, outPath, lockedErr)
	defer restore()

	summary, err := ConvertLineEndingsFile(context.Background(), srcPath, outPath, "LF", FileOptions{})
	assertFinalizeLockFailure(t, outPath, summary.TempPath, summary.ManifestPath, err, lockedErr)
}

func TestConvertEncodingFileReportsLockedOutputDuringFinalize(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lockedErr := errors.New("output is locked")
	restore := installFinalizeLock(t, outPath, lockedErr)
	defer restore()

	summary, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-16LE", FileOptions{})
	assertFinalizeLockFailure(t, outPath, summary.TempPath, summary.ManifestPath, err, lockedErr)
}

func installFinalizeLock(t *testing.T, outputPath string, lockedErr error) func() {
	t.Helper()
	restoreRenamePath := renamePath
	renamePath = func(oldPath string, newPath string) error {
		if filepath.Clean(newPath) == filepath.Clean(outputPath) {
			return lockedErr
		}
		return os.Rename(oldPath, newPath)
	}
	return func() {
		renamePath = restoreRenamePath
	}
}

func assertFinalizeLockFailure(t *testing.T, outputPath string, tempPath string, manifestPath string, runErr error, lockedErr error) {
	t.Helper()
	if !errors.Is(runErr, lockedErr) {
		t.Fatalf("err = %v, want %v", runErr, lockedErr)
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(tempPath); err != nil {
		t.Fatalf("temp output should remain for recovery, stat err = %v", err)
	}
	manifest := readManifest(t, manifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if manifest.Error != lockedErr.Error() {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}
