package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceSnapshotIsInvalidatedByOpenAndClose(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New()
	if _, err := s.Open(rootA); err != nil {
		t.Fatal(err)
	}
	root, generation := s.workspaceSnapshot()
	if err := s.validateWorkspaceSnapshot(root, generation); err != nil {
		t.Fatalf("current snapshot rejected: %v", err)
	}

	if _, err := s.Open(rootB); err != nil {
		t.Fatal(err)
	}
	if err := s.validateWorkspaceSnapshot(root, generation); !errors.Is(err, ErrWorkspaceChanged) {
		t.Fatalf("snapshot after Open error = %v, want ErrWorkspaceChanged", err)
	}
	if entries, err := s.ListDir(""); err != nil || len(entries) != 1 || entries[0].Name != "b.txt" {
		t.Fatalf("ListDir current workspace = %#v, %v", entries, err)
	}
	if content, err := s.ReadFile("b.txt"); err != nil || content.Content != "b" {
		t.Fatalf("ReadFile current workspace = %#v, %v", content, err)
	}

	root, generation = s.workspaceSnapshot()
	s.Close()
	if err := s.validateWorkspaceSnapshot(root, generation); !errors.Is(err, ErrWorkspaceChanged) {
		t.Fatalf("snapshot after Close error = %v, want ErrWorkspaceChanged", err)
	}
}
