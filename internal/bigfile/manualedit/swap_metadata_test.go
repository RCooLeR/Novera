package manualedit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyFileEditSwapOriginalPreservesBackupTimeAndSourceReadOnlyMode(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	originalTime := time.Unix(1_700_000_200, 789_000_000)
	if err := os.WriteFile(srcPath, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(srcPath, originalTime, originalTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(srcPath, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(srcPath, 0o666)
		_ = os.Chmod(backupPath, 0o666)
	})

	summary, err := ApplyFileEdit(context.Background(), srcPath, outPath, Edit{
		Start: 6,
		End:   11,
		Text:  []byte("Quarry"),
	}, FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Swapped {
		t.Fatal("expected swapped summary")
	}

	sourceInfo, err := os.Stat(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !isReadOnlyMode(sourceInfo.Mode()) {
		t.Fatalf("swapped source mode = %v, want read-only permission bits preserved", sourceInfo.Mode().Perm())
	}

	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !isReadOnlyMode(backupInfo.Mode()) {
		t.Fatalf("backup mode = %v, want original read-only permission bits", backupInfo.Mode().Perm())
	}
	assertModTimeClose(t, backupPath, backupInfo.ModTime(), originalTime)
}

func isReadOnlyMode(mode os.FileMode) bool {
	return mode.Perm()&0o222 == 0
}

func assertModTimeClose(t *testing.T, path string, got time.Time, want time.Time) {
	t.Helper()
	if delta := got.Sub(want).Abs(); delta > 2*time.Second {
		t.Fatalf("%s modtime = %s, want near %s", path, got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}
