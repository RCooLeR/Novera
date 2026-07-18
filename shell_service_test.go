package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteTextAtomicReplacesCompleteDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.txt")
	if err := os.WriteFile(path, []byte("previous"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := writeTextAtomic(path, []byte("complete replacement")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "complete replacement" {
		t.Fatalf("destination = %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if gotPerm := info.Mode().Perm(); gotPerm != 0o751 {
			t.Fatalf("destination mode = %o, want 751", gotPerm)
		}
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".novera-export-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary exports left behind: %v", leftovers)
	}
}

func TestWriteTextAtomicRefusesDirectoryDestination(t *testing.T) {
	dir := t.TempDir()
	if err := writeTextAtomic(dir, []byte("must not publish")); err == nil {
		t.Fatal("expected non-regular destination refusal")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("destination directory was damaged: info=%v err=%v", info, err)
	}
}

func TestShellTracksUnsavedResourcesForNativeCloseGate(t *testing.T) {
	shell := &Shell{}
	if shell.hasUnsavedResources() {
		t.Fatal("new shell unexpectedly reports unsaved resources")
	}
	shell.SetUnsavedResources(true)
	if !shell.hasUnsavedResources() {
		t.Fatal("native close gate did not retain dirty state")
	}
	shell.SetUnsavedResources(false)
	if shell.hasUnsavedResources() {
		t.Fatal("native close gate did not clear dirty state")
	}
}
