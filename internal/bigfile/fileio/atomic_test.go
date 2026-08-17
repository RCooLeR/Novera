package fileio

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileAtomicPublishesViaTempAndRemovesTemp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")

	summary, err := WriteFileAtomic(path, []byte("hello"), AtomicWriteOptions{Mode: 0o600})
	if err != nil {
		t.Fatal(err)
	}
	if summary.BytesWritten != int64(len("hello")) {
		t.Fatalf("BytesWritten = %d", summary.BytesWritten)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp stat err = %v, want not exist", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("output = %q", got)
	}
}

func TestWriteFileAtomicRefusesExistingOutputByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp stat err = %v, want not exist", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing" {
		t.Fatalf("existing output changed to %q", got)
	}
}

func TestWriteFileAtomicOverwritesOnlyWhenExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Overwritten {
		t.Fatal("Overwritten = false, want true")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("output = %q, want new", got)
	}
}

func TestWriteFileAtomicOverwritePreservesExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX execute permission preservation is not observable on Windows")
	}
	path := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(path, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{Mode: 0o600, Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %o, want preserved 700", got)
	}
}

func TestWriteFileAtomicRefusesExistingTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	tempPath := path + ".quarry.tmp"
	if err := os.WriteFile(tempPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{})
	if !errors.Is(err, ErrTempExists) {
		t.Fatalf("err = %v, want ErrTempExists", err)
	}
	if got, err := os.ReadFile(tempPath); err != nil || string(got) != "partial" {
		t.Fatalf("temp changed: %q, %v", got, err)
	}
}

func TestWriteFileAtomicRefusesExistingOverwriteBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	backupPath := path + ".quarry.overwrite.bak"
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{Overwrite: true})
	if !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing" {
		t.Fatalf("existing output changed to %q", got)
	}
}

func TestWriteFileAtomicPreservesOrphanedOverwriteBackupForInspection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	backupPath := path + ".quarry.overwrite.bak"
	tempPath := path + ".quarry.tmp"
	if err := os.WriteFile(backupPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempPath, []byte("partial-new"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{Overwrite: true})
	if !errors.Is(err, ErrBackupExists) {
		t.Fatalf("err = %v, want ErrBackupExists", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination stat error = %v, want absent", statErr)
	}
	if got, readErr := os.ReadFile(backupPath); readErr != nil || string(got) != "original" {
		t.Fatalf("backup changed: %q, %v", got, readErr)
	}
	if got, err := os.ReadFile(tempPath); err != nil || string(got) != "partial-new" {
		t.Fatalf("temp should remain for inspection: %q, %v", got, err)
	}
}

func TestRecoverOverwriteBackupNoopsWhenOutputExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(path, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := RecoverOverwriteBackup(path)
	if err != nil {
		t.Fatal(err)
	}
	if recovered {
		t.Fatal("expected no recovery when output exists")
	}
}

func TestWriteFileAtomicRemovesTempOnRenameFailure(t *testing.T) {
	restoreRename := renamePath
	renamePath = func(oldPath, newPath string) error {
		return errors.New("rename failed")
	}
	defer func() {
		renamePath = restoreRename
	}()

	path := filepath.Join(t.TempDir(), "out.txt")
	summary, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{})
	if err == nil {
		t.Fatal("expected rename error")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp stat err = %v, want not exist", err)
	}
}

func TestWriteFileAtomicRestoresExistingOutputOnOverwriteRenameFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	restoreRename := renamePath
	renamePath = func(oldPath, newPath string) error {
		if oldPath == path+".quarry.tmp" {
			return errors.New("publish failed")
		}
		return os.Rename(oldPath, newPath)
	}
	defer func() {
		renamePath = restoreRename
	}()

	summary, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{Overwrite: true})
	if err == nil {
		t.Fatal("expected publish error")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "existing" {
		t.Fatalf("output = %q, want restored existing output", got)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(path + ".quarry.overwrite.bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup stat err = %v, want not exist", err)
	}
}

func TestWriteFileAtomicReportsDirectorySyncFailureAfterPublish(t *testing.T) {
	restoreSyncDir := syncDirPath
	syncDirPath = func(string) error {
		return errors.New("dir sync failed")
	}
	defer func() {
		syncDirPath = restoreSyncDir
	}()

	path := filepath.Join(t.TempDir(), "out.txt")
	summary, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{})
	if err == nil || !strings.Contains(err.Error(), "dir sync failed") {
		t.Fatalf("err = %v, want dir sync failure", err)
	}
	if summary.BytesWritten != int64(len("new")) {
		t.Fatalf("BytesWritten = %d, want %d", summary.BytesWritten, len("new"))
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "new" {
		t.Fatalf("output = %q, want new", got)
	}
}

func TestWriteFileAtomicSyncsDirectoryAfterBackupRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	restoreSyncDir := syncDirPath
	calls := 0
	syncDirPath = func(string) error {
		calls++
		return nil
	}
	defer func() {
		syncDirPath = restoreSyncDir
	}()

	if _, err := WriteFileAtomic(path, []byte("new"), AtomicWriteOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("syncDirPath called %d times, want at least publish and backup-removal syncs", calls)
	}
}
