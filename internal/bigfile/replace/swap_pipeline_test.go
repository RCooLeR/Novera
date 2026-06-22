package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceRegexpFileSwapOriginalRollbackFailureReportsBothErrors(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("hello world hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	swapErr := errors.New("destination is locked")
	rollbackErr := errors.New("rollback failed")
	restore := installSwapFinalizeFailure(t, srcPath, outPath, backupPath, swapErr, rollbackErr)
	defer restore()

	summary, err := ReplaceRegexpFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
	}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
	})
	if err == nil {
		t.Fatal("expected swap finalize failure")
	}
	assertSwapRollbackFailureState(t, srcPath, outPath, backupPath, summary.ManifestPath, err, swapErr, rollbackErr)
}

func TestReplaceBatchPlainFileSwapOriginalRollbackFailureReportsBothErrors(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("hello world hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	swapErr := errors.New("destination is locked")
	rollbackErr := errors.New("rollback failed")
	restore := installSwapFinalizeFailure(t, srcPath, outPath, backupPath, swapErr, rollbackErr)
	defer restore()

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte("hello"), Replace: []byte("bye"), Priority: 0},
	}
	summary, err := ReplaceBatchPlainFile(context.Background(), srcPath, outPath, rules, FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
	}, BatchOptions{
		ChunkSize: 8,
	})
	if err == nil {
		t.Fatal("expected swap finalize failure")
	}
	assertSwapRollbackFailureState(t, srcPath, outPath, backupPath, summary.ManifestPath, err, swapErr, rollbackErr)
}

func TestReplaceBatchRegexpFileSwapOriginalRollbackFailureReportsBothErrors(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("id=41 id=42"), 0o600); err != nil {
		t.Fatal(err)
	}

	swapErr := errors.New("destination is locked")
	rollbackErr := errors.New("rollback failed")
	restore := installSwapFinalizeFailure(t, srcPath, outPath, backupPath, swapErr, rollbackErr)
	defer restore()

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte(`id=(\d+)`), Replace: []byte(`row-$1`), Priority: 0},
	}
	summary, err := ReplaceBatchRegexpFile(context.Background(), srcPath, outPath, rules, FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
	}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
	})
	if err == nil {
		t.Fatal("expected swap finalize failure")
	}
	assertSwapRollbackFailureState(t, srcPath, outPath, backupPath, summary.ManifestPath, err, swapErr, rollbackErr)
}

func TestConvertLineEndingsFileSwapOriginalRollbackFailureReportsBothErrors(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("a\r\nb\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	swapErr := errors.New("destination is locked")
	rollbackErr := errors.New("rollback failed")
	restore := installSwapFinalizeFailure(t, srcPath, outPath, backupPath, swapErr, rollbackErr)
	defer restore()

	summary, err := ConvertLineEndingsFile(context.Background(), srcPath, outPath, "LF", FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
	})
	if err == nil {
		t.Fatal("expected swap finalize failure")
	}
	assertSwapRollbackFailureState(t, srcPath, outPath, backupPath, summary.ManifestPath, err, swapErr, rollbackErr)
}

func TestConvertEncodingFileSwapOriginalRollbackFailureReportsBothErrors(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	swapErr := errors.New("destination is locked")
	rollbackErr := errors.New("rollback failed")
	restore := installSwapFinalizeFailure(t, srcPath, outPath, backupPath, swapErr, rollbackErr)
	defer restore()

	summary, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-16LE", FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
	})
	if err == nil {
		t.Fatal("expected swap finalize failure")
	}
	assertSwapRollbackFailureState(t, srcPath, outPath, backupPath, summary.ManifestPath, err, swapErr, rollbackErr)
}

func installSwapFinalizeFailure(t *testing.T, sourcePath string, outputPath string, backupPath string, swapErr error, rollbackErr error) func() {
	t.Helper()
	restoreRenamePath := renamePath
	renamePath = func(oldPath string, newPath string) error {
		cleanOld := filepath.Clean(oldPath)
		cleanNew := filepath.Clean(newPath)
		if cleanOld == filepath.Clean(outputPath) && cleanNew == filepath.Clean(sourcePath) {
			return swapErr
		}
		if cleanOld == filepath.Clean(backupPath) && cleanNew == filepath.Clean(sourcePath) {
			return rollbackErr
		}
		return os.Rename(oldPath, newPath)
	}
	return func() {
		renamePath = restoreRenamePath
	}
}

func assertSwapRollbackFailureState(t *testing.T, sourcePath string, outputPath string, backupPath string, manifestPath string, runErr error, swapErr error, rollbackErr error) {
	t.Helper()
	if !strings.Contains(runErr.Error(), swapErr.Error()) || !strings.Contains(runErr.Error(), rollbackErr.Error()) {
		t.Fatalf("err = %v", runErr)
	}
	manifest := readManifest(t, manifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if !strings.Contains(manifest.Error, swapErr.Error()) || !strings.Contains(manifest.Error, rollbackErr.Error()) {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
	if _, statErr := os.Stat(sourcePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source should be missing after rollback failure, stat err = %v", statErr)
	}
	if _, statErr := os.Stat(backupPath); statErr != nil {
		t.Fatalf("backup should remain after rollback failure, stat err = %v", statErr)
	}
	if _, statErr := os.Stat(outputPath); statErr != nil {
		t.Fatalf("output should remain after swap failure, stat err = %v", statErr)
	}
}
