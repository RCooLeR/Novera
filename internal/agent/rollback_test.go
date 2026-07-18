package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/workspace"
)

func TestRollbackJournal(t *testing.T) {
	root := t.TempDir()
	ws := workspace.New()
	if _, err := ws.Open(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	j := newRollbackJournal()

	// Overwrite an existing file.
	rbA, err := j.snapshot(ws, "write_file", "write a.txt", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.WriteRaw(ws, "a.txt", []byte("changed")); err != nil {
		t.Fatal(err)
	}
	// Create a new file (didn't exist before).
	rbB, err := j.snapshot(ws, "write_file", "write b.txt", "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.WriteRaw(ws, "b.txt", []byte("new file")); err != nil {
		t.Fatal(err)
	}

	// Undo the overwrite -> prior content restored.
	if _, err := j.rollback(ws, rbA.ID); err != nil {
		t.Fatalf("rollback a: %v", err)
	}
	if got, _, _ := workspace.ReadRaw(ws, "a.txt"); string(got) != "original" {
		t.Errorf("a.txt not restored, got %q", got)
	}

	// Undo the creation -> file removed.
	if _, err := j.rollback(ws, rbB.ID); err != nil {
		t.Fatalf("rollback b: %v", err)
	}
	if _, existed, _ := workspace.ReadRaw(ws, "b.txt"); existed {
		t.Error("b.txt should have been deleted by rollback")
	}

	// Unknown id is an error; journal is now empty.
	if _, err := j.rollback(ws, "rb-999"); err == nil {
		t.Error("expected error for unknown rollback id")
	}
	if n := len(j.list()); n != 0 {
		t.Errorf("journal should be empty after both rollbacks, got %d", n)
	}
}

func TestRollbackMoveRestoresBothEnds(t *testing.T) {
	root := t.TempDir()
	ws := workspace.New()
	if _, err := ws.Open(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "from.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "to.txt"), []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}

	j := newRollbackJournal()
	rb, err := j.snapshot(ws, "move_file", "move", "from.txt", "to.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Rename("from.txt", "to.txt"); err != nil {
		t.Fatal(err)
	}
	// After move: to.txt has payload, from.txt gone.
	if _, err := j.rollback(ws, rb.ID); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got, _, _ := workspace.ReadRaw(ws, "from.txt"); string(got) != "payload" {
		t.Errorf("from.txt not restored, got %q", got)
	}
	if got, _, _ := workspace.ReadRaw(ws, "to.txt"); string(got) != "victim" {
		t.Errorf("to.txt (overwritten dest) not restored, got %q", got)
	}
}

func TestRollbackSnapshotRejectsOversizedFileBeforeMutation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(rollbackMaxFileBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ws := workspace.New()
	if _, err := ws.Open(root); err != nil {
		t.Fatal(err)
	}
	j := newRollbackJournal()
	if _, err := j.snapshot(ws, "delete_file", "delete large.bin", "large.bin"); !errors.Is(err, workspace.ErrRawTooLarge) {
		t.Fatalf("snapshot error = %v, want ErrRawTooLarge", err)
	}
	if len(j.list()) != 0 {
		t.Fatal("failed snapshot must not publish an incomplete undo entry")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("mutation guard damaged source: %v", err)
	}
	if info.Size() != rollbackMaxFileBytes+1 {
		t.Fatalf("source size = %d after refused snapshot", info.Size())
	}
}

func TestRollbackSnapshotRejectsDirectoryAndPublishesNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := workspace.New()
	if _, err := ws.Open(root); err != nil {
		t.Fatal(err)
	}
	j := newRollbackJournal()
	if _, err := j.snapshot(ws, "delete_file", "delete tree", "tree"); !errors.Is(err, workspace.ErrRawNotRegular) {
		t.Fatalf("snapshot error = %v, want ErrRawNotRegular", err)
	}
	if len(j.list()) != 0 {
		t.Fatal("directory snapshot failure must not publish a misleading undo entry")
	}
	if info, err := os.Stat(filepath.Join(root, "tree")); err != nil || !info.IsDir() {
		t.Fatalf("directory was damaged: info=%v err=%v", info, err)
	}
}
