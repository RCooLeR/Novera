package artifacts

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeWS struct{ root string }

func (f fakeWS) Root() string { return f.root }

func TestArtifactLifecycleAndStaleness(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("report.md", "# report")
	write("data.csv", "a,b\n1,2\n")

	s := New(fakeWS{root: root})

	// Register an artifact derived from data.csv.
	a, err := s.CreateArtifact(Artifact{Kind: "report", Title: "Report", Path: "report.md", Sources: []string{"data.csv"}, Tool: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.CreatedAt == 0 {
		t.Fatalf("artifact not initialized: %+v", a)
	}

	list, err := s.ListArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Stale || list[0].Missing {
		t.Fatalf("fresh artifact should be listed and not stale/missing: %+v", list)
	}

	// Re-registering the same path updates in place (no duplicate).
	if _, err := s.CreateArtifact(Artifact{Kind: "report", Title: "Report v2", Path: "report.md"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListArtifacts(); len(l) != 1 || l[0].Title != "Report v2" {
		t.Fatalf("re-register should update in place: %+v", l)
	}

	// Touch the source newer than the artifact -> stale.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(root, "data.csv"), future, future); err != nil {
		t.Fatal(err)
	}
	// Restore the source link that the v2 re-register dropped.
	if _, err := s.CreateArtifact(Artifact{Kind: "report", Title: "Report v3", Path: "report.md", Sources: []string{"data.csv"}}); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListArtifacts(); !l[0].Stale {
		t.Errorf("artifact should be stale when a source is newer: %+v", l[0])
	}

	// Missing content file -> missing flag.
	if err := os.Remove(filepath.Join(root, "report.md")); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListArtifacts(); !l[0].Missing {
		t.Errorf("artifact should be missing when its file is gone: %+v", l[0])
	}

	// Archive / restore / delete.
	id := a.ID
	if err := s.SetArchived(id, true); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetArtifact(id); !g.Archived {
		t.Error("artifact should be archived")
	}
	if err := s.DeleteArtifact(id); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListArtifacts(); len(l) != 0 {
		t.Errorf("artifact should be deleted, got %d", len(l))
	}
}

func TestCreateArtifactRejectsMissingFile(t *testing.T) {
	s := New(fakeWS{root: t.TempDir()})
	if _, err := s.CreateArtifact(Artifact{Kind: "report", Path: "nope.md"}); err == nil {
		t.Error("expected error registering a non-existent file")
	}
}
