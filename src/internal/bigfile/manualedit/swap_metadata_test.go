package manualedit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyFileEditSwapOriginalPreservesReadOnlySourceByRefusingMutation(t *testing.T) {
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
	if !errors.Is(err, ErrSwapOriginalDisabled) {
		t.Fatalf("err = %v, want ErrSwapOriginalDisabled", err)
	}
	if summary.Swapped {
		t.Fatal("disabled source replacement reported a swap")
	}

	sourceInfo, err := os.Stat(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !isReadOnlyMode(sourceInfo.Mode()) {
		t.Fatalf("swapped source mode = %v, want read-only permission bits preserved", sourceInfo.Mode().Perm())
	}

	assertModTimeClose(t, srcPath, sourceInfo.ModTime(), originalTime)
	if got, readErr := os.ReadFile(srcPath); readErr != nil || string(got) != "hello world" {
		t.Fatalf("source changed: %q, %v", got, readErr)
	}
	for _, path := range []string{outPath, backupPath, outPath + ".quarry.tmp", outPath + ".quarry.manifest.json"} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("disabled swap created %q: %v", path, statErr)
		}
	}
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
