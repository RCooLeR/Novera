package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileRange(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("abcdefghij", 1024) // 10240 bytes of known text
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01, 0x02, 'A', 'B'}, 0o644); err != nil {
		t.Fatal(err)
	}
	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}

	c, err := s.ReadFileRange("big.txt", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != int64(len(content)) || c.Length != 100 || c.Binary || c.EOF || c.Text != content[:100] {
		t.Errorf("first page wrong: %+v", c)
	}

	c, err = s.ReadFileRange("big.txt", int64(len(content)-50), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if c.Length != 50 || !c.EOF || c.Text != content[len(content)-50:] {
		t.Errorf("tail page wrong: len=%d eof=%v", c.Length, c.EOF)
	}

	c, err = s.ReadFileRange("big.txt", 99999, 100)
	if err != nil {
		t.Fatal(err)
	}
	if c.Length != 0 || !c.EOF {
		t.Errorf("beyond-end page wrong: len=%d eof=%v", c.Length, c.EOF)
	}

	c, err = s.ReadFileRange("bin.dat", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Binary || c.Text != "" || c.Hex == "" {
		t.Errorf("binary page wrong: %+v", c)
	}
}

func writeFixture(t *testing.T, root, rel string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("package x\n// TODO: do it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The whole-tree walks (ListAllFiles, Search, Diagnostics) must skip noise
// directories so the Problems panel and the agent file tools aren't flooded.
func TestWalksSkipNoiseDirs(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "src/a.go")
	writeFixture(t, root, "node_modules/pkg/index.js")
	writeFixture(t, root, ".gocache/cached.go")
	writeFixture(t, root, "tmp/scratch.go")
	writeFixture(t, root, "build/out.go")

	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatalf("open: %v", err)
	}

	files, err := s.ListAllFiles()
	if err != nil {
		t.Fatalf("ListAllFiles: %v", err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f] = true
	}
	if !got["src/a.go"] {
		t.Errorf("expected src/a.go in listing, got %v", files)
	}
	for _, bad := range []string{"node_modules/pkg/index.js", ".gocache/cached.go", "tmp/scratch.go", "build/out.go"} {
		if got[bad] {
			t.Errorf("noise path %q should have been skipped", bad)
		}
	}

	diag, err := s.Diagnostics()
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	for _, d := range diag.Items {
		if d.Path != "src/a.go" {
			t.Errorf("diagnostic surfaced from a noise dir: %s", d.Path)
		}
	}

	res, err := s.Search("package", false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, m := range res.Matches {
		if m.Path != "src/a.go" {
			t.Errorf("search match surfaced from a noise dir: %s", m.Path)
		}
	}
}

// The UI file tree (ListDir) is single-level and must still show everything,
// including noise dirs, so the explorer is complete.
func TestListDirShowsEverything(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "src/a.go")
	writeFixture(t, root, "node_modules/pkg/index.js")
	writeFixture(t, root, "tmp/scratch.go")

	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatalf("open: %v", err)
	}
	entries, err := s.ListDir("")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name] = true
	}
	for _, want := range []string{"src", "node_modules", "tmp"} {
		if !seen[want] {
			t.Errorf("UI tree should show %q; saw %v", want, seen)
		}
	}
}
