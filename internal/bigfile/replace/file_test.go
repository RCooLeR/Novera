package replace

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReplacePlainFileWritesOutputAndManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("hello world hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != 2 {
		t.Fatalf("matches = %d, want 2", summary.Matches)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bye world bye" {
		t.Fatalf("output = %q", string(got))
	}

	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(src) != "hello world hello" {
		t.Fatalf("source changed to %q", string(src))
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp path should be gone, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "complete" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if manifest.Matches != 2 {
		t.Fatalf("manifest matches = %d", manifest.Matches)
	}
	if manifest.SourceSize != int64(len("hello world hello")) {
		t.Fatalf("source size = %d", manifest.SourceSize)
	}
}

func TestReplacePlainFileDoesNotOverwriteOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{}); err == nil {
		t.Fatal("expected overwrite error")
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing" {
		t.Fatalf("output changed to %q", string(got))
	}
}

func TestReplacePlainFileRejectsSourceOutputMatch(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ReplacePlainFile(context.Background(), srcPath, srcPath, []byte("hello"), []byte("bye"), FileOptions{}); err == nil {
		t.Fatal("expected same-path error")
	}
	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("source changed to %q", string(got))
	}
}

func TestReplacePlainFileDoesNotOverwriteManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	manifestPath := outPath + ".quarry.manifest.json"
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{})
	if err == nil {
		t.Fatal("expected manifest exists error")
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp should be cleaned after manifest error, stat err = %v", err)
	}
	got, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing" {
		t.Fatalf("manifest changed to %q", string(got))
	}
}

func TestReplacePlainFileCancelDeletesPartialWhenRequested(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("hello ", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ReplacePlainFile(ctx, srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		ChunkSize:             32,
		DeletePartialOnCancel: true,
		Progress: func(Progress) {
			if !canceled {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial temp should be deleted, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
}

func TestReplacePlainFileCancelKeepsPartialWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("hello ", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ReplacePlainFile(ctx, srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		ChunkSize:             32,
		DeletePartialOnCancel: false,
		Progress: func(Progress) {
			if !canceled {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); err != nil {
		t.Fatalf("partial temp should be preserved, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if !strings.Contains(manifest.Error, context.Canceled.Error()) {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}

func TestReplacePlainFileSwapOriginalCreatesBackup(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("hello world hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		ChunkSize:    5,
		SwapOriginal: true,
		BackupPath:   backupPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Swapped {
		t.Fatal("expected swapped summary")
	}
	if summary.BackupPath != backupPath {
		t.Fatalf("backup path = %q, want %q", summary.BackupPath, backupPath)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("intermediate output should be moved to source, stat err = %v", err)
	}

	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bye world bye" {
		t.Fatalf("source after swap = %q", string(got))
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "hello world hello" {
		t.Fatalf("backup = %q", string(backup))
	}

	manifest := readManifest(t, summary.ManifestPath)
	if !manifest.Swapped {
		t.Fatal("expected swapped manifest")
	}
	if manifest.Backup != backupPath {
		t.Fatalf("manifest backup = %q, want %q", manifest.Backup, backupPath)
	}
}

func TestReplacePlainFileSwapOriginalDoesNotOverwriteBackup(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("existing backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
	}); err == nil {
		t.Fatal("expected backup overwrite error")
	}

	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("source changed to %q", string(got))
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not be written after backup guard, stat err = %v", err)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "existing backup" {
		t.Fatalf("backup changed to %q", string(backup))
	}
}

func TestReplacePlainFileReportsPermissionDeniedWhenTempCreateFails(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}

	restoreOpenExclusive := openExclusive
	openExclusive = func(path string) (syncWriteCloser, error) {
		return nil, fs.ErrPermission
	}
	t.Cleanup(func() {
		openExclusive = restoreOpenExclusive
	})

	_, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{})
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v, want permission denied", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
}

func TestReplacePlainFileReportsDiskFullAndPersistsFailedManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("hello world ", 16)), 0o600); err != nil {
		t.Fatal(err)
	}

	diskFullErr := errors.New("no space left on device")
	restoreOpenExclusive := openExclusive
	openExclusive = func(path string) (syncWriteCloser, error) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		return &failingWriteFile{File: f, failAfter: 24, err: diskFullErr}, nil
	}
	t.Cleanup(func() {
		openExclusive = restoreOpenExclusive
	})

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{ChunkSize: 8})
	if !errors.Is(err, diskFullErr) {
		t.Fatalf("err = %v, want disk full", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); err != nil {
		t.Fatalf("temp output should remain for recovery, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if manifest.Error != diskFullErr.Error() {
		t.Fatalf("manifest error = %q, want %q", manifest.Error, diskFullErr.Error())
	}
}

func TestReplacePlainFileFailsIfSourceChangesBeforeSwap(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("hello world ", 32)), 0o600); err != nil {
		t.Fatal(err)
	}

	changed := false
	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		ChunkSize:    16,
		SwapOriginal: true,
		BackupPath:   backupPath,
		Progress: func(Progress) {
			if changed {
				return
			}
			changed = true
			time.Sleep(10 * time.Millisecond)
			if err := os.WriteFile(srcPath, []byte("source changed externally"), 0o600); err != nil {
				t.Fatalf("mutate source: %v", err)
			}
		},
	})
	if !errors.Is(err, ErrSourceModifiedDuringOperation) {
		t.Fatalf("err = %v, want %v", err, ErrSourceModifiedDuringOperation)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup should not exist, stat err = %v", err)
	}
	got, readErr := os.ReadFile(srcPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "source changed externally" {
		t.Fatalf("source = %q", string(got))
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if manifest.Error != ErrSourceModifiedDuringOperation.Error() {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}

func TestReplacePlainFileReportsLockedOutputDuringFinalize(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}

	lockedErr := errors.New("output is locked")
	restoreRenamePath := renamePath
	renamePath = func(oldPath string, newPath string) error {
		if filepath.Clean(newPath) == filepath.Clean(outPath) {
			return lockedErr
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		renamePath = restoreRenamePath
	})

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{})
	if !errors.Is(err, lockedErr) {
		t.Fatalf("err = %v, want locked output error", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); err != nil {
		t.Fatalf("temp output should remain for recovery, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if manifest.Error != lockedErr.Error() {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}

func TestReplacePlainFileSwapOriginalRollbackFailureReportsBothErrors(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("hello world hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	swapErr := errors.New("destination is locked")
	rollbackErr := errors.New("rollback failed")
	restoreRenamePath := renamePath
	renamePath = func(oldPath string, newPath string) error {
		cleanOld := filepath.Clean(oldPath)
		cleanNew := filepath.Clean(newPath)
		if cleanOld == filepath.Clean(outPath) && cleanNew == filepath.Clean(srcPath) {
			return swapErr
		}
		if cleanOld == filepath.Clean(backupPath) && cleanNew == filepath.Clean(srcPath) {
			return rollbackErr
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		renamePath = restoreRenamePath
	})

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		ChunkSize:    5,
		SwapOriginal: true,
		BackupPath:   backupPath,
	})
	if err == nil {
		t.Fatal("expected swap finalize failure")
	}
	if !strings.Contains(err.Error(), swapErr.Error()) || !strings.Contains(err.Error(), rollbackErr.Error()) {
		t.Fatalf("err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if !strings.Contains(manifest.Error, swapErr.Error()) || !strings.Contains(manifest.Error, rollbackErr.Error()) {
		t.Fatalf("manifest error = %q", manifest.Error)
	}

	if _, statErr := os.Stat(srcPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source should be missing after rollback failure, stat err = %v", statErr)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "hello world hello" {
		t.Fatalf("backup = %q", string(backup))
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "bye world bye" {
		t.Fatalf("output = %q", string(out))
	}
}

type failingWriteFile struct {
	*os.File
	failAfter int64
	written   int64
	err       error
}

func (f *failingWriteFile) Write(p []byte) (int, error) {
	if f.written >= f.failAfter {
		return 0, f.err
	}
	remaining := f.failAfter - f.written
	if remaining < int64(len(p)) {
		n, err := f.File.Write(p[:remaining])
		f.written += int64(n)
		if err != nil {
			return n, err
		}
		return n, f.err
	}
	n, err := f.File.Write(p)
	f.written += int64(n)
	return n, err
}

func readManifest(t *testing.T, path string) Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}
