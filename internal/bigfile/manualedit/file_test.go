package manualedit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyFileEditWritesNewOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "edited.txt")
	if err := os.WriteFile(srcPath, []byte("alpha bravo charlie"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ApplyFileEdit(context.Background(), srcPath, outPath, Edit{
		Start: 6,
		End:   11,
		Text:  []byte("delta"),
	}, FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.BytesWritten != int64(len("alpha delta charlie")) {
		t.Fatalf("bytes written = %d", summary.BytesWritten)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha delta charlie" {
		t.Fatalf("output = %q", string(got))
	}
}

func TestApplyFileEditSwapOriginalFailsClosed(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "edited.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ApplyFileEdit(context.Background(), srcPath, outPath, Edit{
		Start: 5,
		End:   5,
		Text:  []byte(" brave"),
	}, FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
	})
	if !errors.Is(err, ErrSwapOriginalDisabled) {
		t.Fatalf("err = %v, want ErrSwapOriginalDisabled", err)
	}
	if summary.OutputPath != outPath || summary.Swapped {
		t.Fatalf("summary = %+v", summary)
	}

	current, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "hello world" {
		t.Fatalf("source = %q", string(current))
	}
	for _, path := range []string{outPath, backupPath, outPath + ".quarry.tmp", outPath + ".quarry.manifest.json"} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("disabled swap created %q: %v", path, statErr)
		}
	}
}

func TestApplyFileEditCancelDeletesPartialOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "edited.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("abcdef", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	summary, err := ApplyFileEdit(ctx, srcPath, outPath, Edit{
		Start: 0,
		End:   0,
		Text:  []byte("prefix\n"),
	}, FileOptions{
		DeletePartialOnCancel: true,
		Progress: func(Progress) {
			cancel()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp output should be deleted, stat err = %v", err)
	}
	manifest := readManualEditManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" || !strings.Contains(manifest.Error, context.Canceled.Error()) {
		t.Fatalf("manifest status/error = %q/%q, want canceled", manifest.Status, manifest.Error)
	}
}

func TestApplyFileEditCancelKeepsPartialOutputWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "edited.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("abcdef", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	summary, err := ApplyFileEdit(ctx, srcPath, outPath, Edit{
		Start: 0,
		End:   0,
		Text:  []byte("prefix\n"),
	}, FileOptions{
		DeletePartialOnCancel: false,
		Progress: func(Progress) {
			cancel()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(summary.TempPath); err != nil {
		t.Fatalf("temp output should be preserved, stat err = %v", err)
	}
	manifest := readManualEditManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" || !strings.Contains(manifest.Error, context.Canceled.Error()) {
		t.Fatalf("manifest status/error = %q/%q, want canceled", manifest.Status, manifest.Error)
	}
}

func TestApplyFileEditRejectsOversizedInsertedText(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "edited.txt")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ApplyFileEdit(context.Background(), srcPath, outPath, Edit{
		Start: 0,
		End:   0,
		Text:  []byte("toolong"),
	}, FileOptions{
		MaxInsertedBytes: 4,
	})
	if !errors.Is(err, ErrInsertedTextTooLarge) {
		t.Fatalf("err = %v, want ErrInsertedTextTooLarge", err)
	}
}

func TestApplyFileEditRenameFailureLeavesTempAndFailedManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "edited.txt")
	if err := os.WriteFile(srcPath, []byte("alpha bravo charlie"), 0o600); err != nil {
		t.Fatal(err)
	}

	renameErr := errors.New("forced rename failure")
	oldRenamePath := renamePath
	renamePath = func(old string, new string) error {
		if old == outPath+".quarry.tmp" && new == outPath {
			return renameErr
		}
		return oldRenamePath(old, new)
	}
	t.Cleanup(func() {
		renamePath = oldRenamePath
	})

	summary, err := ApplyFileEdit(context.Background(), srcPath, outPath, Edit{
		Start: 6,
		End:   11,
		Text:  []byte("delta"),
	}, FileOptions{})
	if !errors.Is(err, renameErr) {
		t.Fatalf("err = %v, want forced rename failure", err)
	}
	assertFileContent(t, srcPath, "alpha bravo charlie")
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final output should not exist after rename failure, stat err = %v", err)
	}
	assertFileContent(t, summary.TempPath, "alpha delta charlie")

	manifest := readManualEditManifest(t, summary.ManifestPath)
	if manifest.Status != "failed" || !strings.Contains(manifest.Error, renameErr.Error()) {
		t.Fatalf("manifest status/error = %q/%q, want failed rename", manifest.Status, manifest.Error)
	}
}

func TestManualEditSaveRejectsBackupPathMatchingSource(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(srcPath, []byte("alpha bravo charlie"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ApplyFileEdit(context.Background(), srcPath, filepath.Join(dir, "edited.txt"), Edit{
		Start: 6,
		End:   11,
		Text:  []byte("delta"),
	}, FileOptions{
		SwapOriginal: true,
		BackupPath:   srcPath,
	})
	if !errors.Is(err, ErrSwapOriginalDisabled) {
		t.Fatalf("ApplyFileEdit err = %v, want ErrSwapOriginalDisabled", err)
	}

	session := NewSession(int64(len("alpha bravo charlie")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 6, End: 11, Text: []byte("delta")}); err != nil {
		t.Fatal(err)
	}
	_, err = WriteSessionToFile(context.Background(), srcPath, filepath.Join(dir, "session.txt"), session, FileOptions{
		SwapOriginal: true,
		BackupPath:   srcPath,
	})
	if !errors.Is(err, ErrSwapOriginalDisabled) {
		t.Fatalf("WriteSessionToFile err = %v, want ErrSwapOriginalDisabled", err)
	}
}

func TestWriteSessionToFileWritesStagedEdits(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(srcPath, []byte("alpha bravo charlie"), 0o600); err != nil {
		t.Fatal(err)
	}

	session := NewSession(int64(len("alpha bravo charlie")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 6, End: 11, Text: []byte("delta")}); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 0, End: 0, Text: []byte(">> ")}); err != nil {
		t.Fatal(err)
	}

	summary, err := WriteSessionToFile(context.Background(), srcPath, outPath, session, FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.BytesWritten != int64(len(">> alpha delta charlie")) {
		t.Fatalf("bytes written = %d", summary.BytesWritten)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != ">> alpha delta charlie" {
		t.Fatalf("output = %q", string(got))
	}
}

// If the source file changes size between staging and the copy-through save, the
// piece-table offsets are stale; the save must fail (not silently write a
// truncated copy and report success) and must not leave a finished output.
func TestWriteSessionToFileFailsWhenSourceSizeChanged(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "session.txt")
	original := "alpha bravo charlie delta"
	if err := os.WriteFile(srcPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	session := NewSession(int64(len(original)), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 0, End: 0, Text: []byte(">> ")}); err != nil {
		t.Fatal(err)
	}

	// Source shrinks after staging (e.g. re-exported / truncated dump).
	if err := os.WriteFile(srcPath, []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := WriteSessionToFile(context.Background(), srcPath, outPath, session, FileOptions{})
	if !errors.Is(err, ErrSourceModifiedDuringOperation) {
		t.Fatalf("err = %v, want ErrSourceModifiedDuringOperation", err)
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("output file should not exist after a failed save, stat err = %v", statErr)
	}
}

func TestWriteSessionToFileCancelDeletesPartialOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("abcdef", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}

	session := NewSession(int64(len(strings.Repeat("abcdef", 4096))), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 0, End: 0, Text: []byte("prefix\n")}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	summary, err := WriteSessionToFile(ctx, srcPath, outPath, session, FileOptions{
		DeletePartialOnCancel: true,
		Progress: func(Progress) {
			cancel()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp output should be deleted, stat err = %v", err)
	}
	manifest := readManualEditManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" || !strings.Contains(manifest.Error, context.Canceled.Error()) {
		t.Fatalf("manifest status/error = %q/%q, want canceled", manifest.Status, manifest.Error)
	}
}

func TestWriteSessionToFileCancelKeepsPartialOutputWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "session.txt")
	content := strings.Repeat("abcdef", 4096)
	if err := os.WriteFile(srcPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	session := NewSession(int64(len(content)), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 0, End: 0, Text: []byte("prefix\n")}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	summary, err := WriteSessionToFile(ctx, srcPath, outPath, session, FileOptions{
		DeletePartialOnCancel: false,
		Progress: func(Progress) {
			cancel()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(summary.TempPath); err != nil {
		t.Fatalf("temp output should be preserved, stat err = %v", err)
	}
	manifest := readManualEditManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" || !strings.Contains(manifest.Error, context.Canceled.Error()) {
		t.Fatalf("manifest status/error = %q/%q, want canceled", manifest.Status, manifest.Error)
	}
}

func TestWriteSessionToFileRenameFailureLeavesTempAndFailedManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "session.txt")
	if err := os.WriteFile(srcPath, []byte("alpha bravo charlie"), 0o600); err != nil {
		t.Fatal(err)
	}

	session := NewSession(int64(len("alpha bravo charlie")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 6, End: 11, Text: []byte("delta")}); err != nil {
		t.Fatal(err)
	}

	renameErr := errors.New("forced rename failure")
	oldRenamePath := renamePath
	renamePath = func(old string, new string) error {
		if old == outPath+".quarry.tmp" && new == outPath {
			return renameErr
		}
		return oldRenamePath(old, new)
	}
	t.Cleanup(func() {
		renamePath = oldRenamePath
	})

	summary, err := WriteSessionToFile(context.Background(), srcPath, outPath, session, FileOptions{})
	if !errors.Is(err, renameErr) {
		t.Fatalf("err = %v, want forced rename failure", err)
	}
	assertFileContent(t, srcPath, "alpha bravo charlie")
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final output should not exist after rename failure, stat err = %v", err)
	}
	assertFileContent(t, summary.TempPath, "alpha delta charlie")

	manifest := readManualEditManifest(t, summary.ManifestPath)
	if manifest.Status != "failed" || !strings.Contains(manifest.Error, renameErr.Error()) {
		t.Fatalf("manifest status/error = %q/%q, want failed rename", manifest.Status, manifest.Error)
	}
}

func TestWriteSessionToFileSwapOriginalFailsBeforeFilesystemMutation(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "session.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte("alpha bravo charlie"), 0o600); err != nil {
		t.Fatal(err)
	}

	session := NewSession(int64(len("alpha bravo charlie")), DefaultMaxInsertedBytes)
	if err := session.ApplyEdit(Edit{Start: 6, End: 11, Text: []byte("delta")}); err != nil {
		t.Fatal(err)
	}

	summary, err := WriteSessionToFile(context.Background(), srcPath, outPath, session, FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
	})
	if !errors.Is(err, ErrSwapOriginalDisabled) {
		t.Fatalf("err = %v, want ErrSwapOriginalDisabled", err)
	}
	if summary.Swapped {
		t.Fatal("summary should not report swapped")
	}
	assertFileContent(t, srcPath, "alpha bravo charlie")
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup should not exist, stat err = %v", err)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}

func TestManifestProgressFlushPolicy(t *testing.T) {
	started := time.Unix(100, 0)
	if shouldFlushManifestProgress(0, 0, started, started.Add(10*manifestProgressFlushInterval)) {
		t.Fatal("zero progress should not flush")
	}
	if !shouldFlushManifestProgress(manifestProgressFlushBytes, 0, started, started) {
		t.Fatal("byte threshold should flush")
	}
	if !shouldFlushManifestProgress(1, 0, started, started.Add(manifestProgressFlushInterval)) {
		t.Fatal("interval threshold should flush")
	}
	if shouldFlushManifestProgress(1, 0, started, started.Add(manifestProgressFlushInterval-time.Nanosecond)) {
		t.Fatal("progress below byte and interval thresholds should not flush")
	}
}

func readManualEditManifest(t *testing.T, path string) Manifest {
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

type fileInfoWithModTime struct {
	os.FileInfo
	modTime time.Time
}

func (f fileInfoWithModTime) ModTime() time.Time {
	return f.modTime
}
