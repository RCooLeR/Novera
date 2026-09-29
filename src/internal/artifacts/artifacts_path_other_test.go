//go:build !windows

package artifacts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryAcceptsCanonicalWorkspaceRootAlias(t *testing.T) {
	realRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "report.md"), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(fakeWS{root: alias})
	if _, err := s.CreateArtifact(Artifact{Kind: "report", Path: "report.md"}); err != nil {
		t.Fatalf("CreateArtifact through canonical workspace alias: %v", err)
	}
	if _, err := os.Stat(filepath.Join(realRoot, ".novera", "artifacts.json")); err != nil {
		t.Fatalf("stat canonical artifact registry: %v", err)
	}
}
