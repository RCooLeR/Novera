package fileio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenExclusiveOutputWritesAndRefusesExistingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")

	out, err := OpenExclusiveOutput(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if out.Path() != path {
		t.Fatalf("Path() = %q, want %q", out.Path(), path)
	}
	if _, err := out.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := out.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("output = %q, want hello", got)
	}

	_, err = OpenExclusiveOutput(path, 0o600)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
}

func TestExclusiveOutputCleanupClosesAndRemovesPartialOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	out, err := OpenExclusiveOutput(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}

	if err := out.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat err = %v, want not exist", err)
	}
	if err := out.Cleanup(); err != nil {
		t.Fatalf("second cleanup err = %v", err)
	}
}

func TestExclusiveOutputCleanupReportsRemoveFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	out, err := OpenExclusiveOutput(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	restoreRemove := removePath
	removePath = func(string) error {
		return errors.New("remove denied")
	}
	defer func() {
		removePath = restoreRemove
	}()

	err = out.Cleanup()
	if err == nil {
		t.Fatal("expected cleanup error")
	}
	if !strings.Contains(err.Error(), "remove denied") {
		t.Fatalf("err = %v, want remove failure context", err)
	}
}

func TestExclusiveOutputCloseReportsDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	out, err := OpenExclusiveOutput(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}

	restoreSyncDir := syncDirPath
	syncDirPath = func(string) error {
		return errors.New("dir sync failed")
	}
	defer func() {
		syncDirPath = restoreSyncDir
	}()

	err = out.Close()
	if err == nil || !strings.Contains(err.Error(), "dir sync failed") {
		t.Fatalf("err = %v, want dir sync failure", err)
	}
}

func TestExclusiveOutputCleanupSyncsDirectoryAfterRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	out, err := OpenExclusiveOutput(path, 0o600)
	if err != nil {
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

	if err := out.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("syncDirPath called %d times, want close and remove syncs", calls)
	}
}
