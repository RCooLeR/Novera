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

func TestReplacePlainFileSwapOriginalFailsClosedWithoutArtifacts(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	original := []byte("hello world hello")
	if err := os.WriteFile(srcPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

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
	if got, readErr := os.ReadFile(srcPath); readErr != nil || string(got) != string(original) {
		t.Fatalf("source = %q, %v; want unchanged", got, readErr)
	}
	for _, path := range []string{outPath, backupPath, outPath + ".quarry.tmp", outPath + ".quarry.manifest.json"} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("disabled swap created %q: %v", path, statErr)
		}
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
	}); !errors.Is(err, ErrSwapOriginalDisabled) {
		t.Fatalf("error = %v, want ErrSwapOriginalDisabled", err)
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

func TestReplacePlainFileSwapOriginalFailsBeforeProgress(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	original := []byte(strings.Repeat("hello world ", 32))
	if err := os.WriteFile(srcPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	progressCalled := false
	_, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		ChunkSize:    16,
		SwapOriginal: true,
		BackupPath:   backupPath,
		Progress: func(Progress) {
			progressCalled = true
		},
	})
	if !errors.Is(err, ErrSwapOriginalDisabled) {
		t.Fatalf("error = %v, want ErrSwapOriginalDisabled", err)
	}
	if progressCalled {
		t.Fatal("disabled swap invoked progress callback")
	}
	if got, readErr := os.ReadFile(srcPath); readErr != nil || string(got) != string(original) {
		t.Fatalf("source = %q, %v; want unchanged", got, readErr)
	}
	for _, path := range []string{outPath, backupPath, outPath + ".quarry.tmp", outPath + ".quarry.manifest.json"} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("disabled swap created %q: %v", path, statErr)
		}
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

func TestReplacePlainFileSwapOriginalDoesNotReachRenameSeam(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	original := []byte("hello world hello")
	if err := os.WriteFile(srcPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	renameCalled := false
	restoreRenamePath := renamePath
	renamePath = func(oldPath string, newPath string) error {
		renameCalled = true
		return errors.New("rename must not be reached")
	}
	t.Cleanup(func() {
		renamePath = restoreRenamePath
	})

	_, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{
		ChunkSize:    5,
		SwapOriginal: true,
		BackupPath:   backupPath,
	})
	if !errors.Is(err, ErrSwapOriginalDisabled) {
		t.Fatalf("error = %v, want ErrSwapOriginalDisabled", err)
	}
	if renameCalled {
		t.Fatal("disabled swap reached rename seam")
	}
	if got, readErr := os.ReadFile(srcPath); readErr != nil || string(got) != string(original) {
		t.Fatalf("source = %q, %v; want unchanged", got, readErr)
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
