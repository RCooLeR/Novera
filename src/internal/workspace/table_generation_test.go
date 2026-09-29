package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStaleTableCacheInstallCannotOverwriteCurrentWorkspace(t *testing.T) {
	rootA := tableGenerationWorkspace(t, "a.csv", "id,name\n1,Ada\n")
	rootB := tableGenerationWorkspace(t, "b.csv", "id,name\n2,Grace\n")

	s := New()
	if _, err := s.Open(rootA); err != nil {
		t.Fatal(err)
	}
	oldRoot, oldGeneration := s.workspaceSnapshot()

	if _, err := s.Open(rootB); err != nil {
		t.Fatal(err)
	}
	currentRoot, currentGeneration := s.workspaceSnapshot()
	currentResult := &tableResult{key: "workspace-b"}
	currentIndex := &tableIndex{key: "workspace-b"}
	if err := s.installTableResult(currentRoot, currentGeneration, currentResult); err != nil {
		t.Fatal(err)
	}
	if err := s.installTableIndex(currentRoot, currentGeneration, currentIndex); err != nil {
		t.Fatal(err)
	}

	if err := s.installTableResult(oldRoot, oldGeneration, &tableResult{key: "workspace-a"}); !errors.Is(err, ErrWorkspaceChanged) {
		t.Fatalf("stale result install error = %v, want ErrWorkspaceChanged", err)
	}
	if err := s.installTableIndex(oldRoot, oldGeneration, &tableIndex{key: "workspace-a"}); !errors.Is(err, ErrWorkspaceChanged) {
		t.Fatalf("stale index install error = %v, want ErrWorkspaceChanged", err)
	}

	s.tableMu.Lock()
	defer s.tableMu.Unlock()
	if s.tableCache != currentResult {
		t.Fatalf("table result = %#v, want current workspace result", s.tableCache)
	}
	if s.tableIdx != currentIndex {
		t.Fatalf("table index = %#v, want current workspace index", s.tableIdx)
	}
}

func TestStaleTableBrowseCannotReplaceCurrentWorkspaceCursor(t *testing.T) {
	rootA := tableGenerationWorkspace(t, "table.csv", "id,name\n1,Ada\n")
	rootB := tableGenerationWorkspace(t, "table.csv", "id,name\n2,Grace\n3,Linus\n")

	s := New()
	t.Cleanup(s.Close)
	if _, err := s.Open(rootA); err != nil {
		t.Fatal(err)
	}
	oldRoot, oldGeneration := s.workspaceSnapshot()
	oldAbs := filepath.Join(rootA, "table.csv")

	if _, err := s.Open(rootB); err != nil {
		t.Fatal(err)
	}
	page, err := s.QueryTable("table.csv", TableQuery{Limit: 1, SortCol: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Rows[0][1] != "Grace" {
		t.Fatalf("current workspace page = %#v", page.Rows)
	}
	s.tableMu.Lock()
	currentCursor := s.browseCur
	s.tableMu.Unlock()
	if currentCursor == nil {
		t.Fatal("expected current workspace browse cursor")
	}

	_, err = s.tableBrowse(oldRoot, oldGeneration, oldAbs, "table.csv", "", 0, 1, TablePage{})
	if !errors.Is(err, ErrWorkspaceChanged) {
		t.Fatalf("stale browse error = %v, want ErrWorkspaceChanged", err)
	}
	s.tableMu.Lock()
	defer s.tableMu.Unlock()
	if s.browseCur != currentCursor {
		t.Fatal("stale browse replaced the current workspace cursor")
	}
}

func tableGenerationWorkspace(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
