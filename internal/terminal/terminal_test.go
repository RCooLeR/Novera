package terminal

import (
	"path/filepath"
	"testing"
)

type fixedRoot string

func (root fixedRoot) Root() string { return string(root) }

func TestStartRejectsMissingWorkspaceBeforeLaunchingShell(t *testing.T) {
	for _, root := range []fixedRoot{"", fixedRoot(filepath.Join(t.TempDir(), "missing"))} {
		s := New(root)
		if id, err := s.Start(80, 24); err == nil || id != "" {
			t.Fatalf("Start with root %q = (%q, %v), want a pre-launch error", root, id, err)
		}
		if len(s.sessions) != 0 {
			t.Fatal("failed terminal start registered a live session")
		}
	}
}
