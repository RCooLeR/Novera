package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplacePlainFileSwapOriginalPreservesReadOnlySourceByRefusingMutation(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	originalTime := time.Unix(1_700_000_000, 123_000_000)
	if err := os.WriteFile(srcPath, []byte("hello world hello"), 0o600); err != nil {
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

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		ChunkSize:    5,
		SwapOriginal: true,
		BackupPath:   backupPath,
	})
	if !errors.Is(err, ErrSwapOriginalDisabled) {
		t.Fatalf("error = %v, want ErrSwapOriginalDisabled", err)
	}
	if summary != (FileSummary{}) {
		t.Fatalf("summary = %+v, want zero summary", summary)
	}

	sourceInfo, err := os.Stat(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !isReadOnlyMode(sourceInfo.Mode()) {
		t.Fatalf("swapped source mode = %v, want read-only permission bits preserved", sourceInfo.Mode().Perm())
	}

	assertModTimeClose(t, srcPath, sourceInfo.ModTime(), originalTime)
	if got, readErr := os.ReadFile(srcPath); readErr != nil || string(got) != "hello world hello" {
		t.Fatalf("source changed: %q, %v", got, readErr)
	}
	for _, path := range []string{outPath, backupPath, outPath + ".quarry.tmp", outPath + ".quarry.manifest.json"} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("disabled swap created %q: %v", path, statErr)
		}
	}
}

func TestResumeRecoverySourceMissingPreservesBackupAndOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	backupPath := filepath.Join(dir, "source.sql.quarry.bak")
	manifestPath := outputPath + ".quarry.manifest.json"
	originalTime := time.Unix(1_700_000_100, 456_000_000)
	if err := os.WriteFile(backupPath, []byte("old source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(backupPath, originalTime, originalTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(backupPath, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("new source"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(backupPath, 0o666)
	})

	if err := writeManifest(manifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		Phase:          "output_written",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		SourceSize:     int64(len("old source")),
		BytesProcessed: int64(len("old source")),
		BackupPlanned:  backupPath,
		SwapRequested:  true,
		Status:         "failed",
		Error:          "interrupted after backup rename",
	}, true); err != nil {
		t.Fatal(err)
	}
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeRecovery(state); !errors.Is(err, ErrRecoveryMutationDisabled) {
		t.Fatalf("ResumeRecovery error = %v", err)
	}
	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source path was created: %v", err)
	}
	if got, err := os.ReadFile(outputPath); err != nil || string(got) != "new source" {
		t.Fatalf("output changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(backupPath); err != nil || string(got) != "old source" {
		t.Fatalf("backup changed: %q, %v", got, err)
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !isReadOnlyMode(backupInfo.Mode()) {
		t.Fatalf("backup mode = %v, want read-only", backupInfo.Mode().Perm())
	}
	assertModTimeClose(t, backupPath, backupInfo.ModTime(), originalTime)
	if got, err := os.ReadFile(manifestPath); err != nil || string(got) != string(manifestBefore) {
		t.Fatalf("manifest changed: %q, %v", got, err)
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
