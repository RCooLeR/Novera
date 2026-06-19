package agent

import (
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
	rbA := j.snapshot(ws, "write_file", "write a.txt", "a.txt")
	if err := ws.WriteRaw("a.txt", []byte("changed")); err != nil {
		t.Fatal(err)
	}
	// Create a new file (didn't exist before).
	rbB := j.snapshot(ws, "write_file", "write b.txt", "b.txt")
	if err := ws.WriteRaw("b.txt", []byte("new file")); err != nil {
		t.Fatal(err)
	}

	// Undo the overwrite -> prior content restored.
	if _, err := j.rollback(ws, rbA.ID); err != nil {
		t.Fatalf("rollback a: %v", err)
	}
	if got, _, _ := ws.ReadRaw("a.txt"); string(got) != "original" {
		t.Errorf("a.txt not restored, got %q", got)
	}

	// Undo the creation -> file removed.
	if _, err := j.rollback(ws, rbB.ID); err != nil {
		t.Fatalf("rollback b: %v", err)
	}
	if _, existed, _ := ws.ReadRaw("b.txt"); existed {
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
	rb := j.snapshot(ws, "move_file", "move", "from.txt", "to.txt")
	if err := ws.Rename("from.txt", "to.txt"); err != nil {
		t.Fatal(err)
	}
	// After move: to.txt has payload, from.txt gone.
	if _, err := j.rollback(ws, rb.ID); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got, _, _ := ws.ReadRaw("from.txt"); string(got) != "payload" {
		t.Errorf("from.txt not restored, got %q", got)
	}
	if got, _, _ := ws.ReadRaw("to.txt"); string(got) != "victim" {
		t.Errorf("to.txt (overwritten dest) not restored, got %q", got)
	}
}
