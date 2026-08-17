package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRegularFileBoundedEnforcesLimitDuringAcquisition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(path, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readRegularFileBounded(path, 3); !errors.Is(err, ErrRawTooLarge) {
		t.Fatalf("bounded read error = %v, want ErrRawTooLarge", err)
	}
	got, _, err := readRegularFileBounded(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcd" {
		t.Fatalf("bounded read = %q", got)
	}
}

func TestReadRegularFileBoundedRejectsLinkedPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("outside the opened identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	if _, _, err := readRegularFileBounded(link, 1024); !errors.Is(err, ErrRawNotRegular) {
		t.Fatalf("linked read error = %v, want ErrRawNotRegular", err)
	}
}

func TestReadFileReportsSparseOversizedFileWithoutReadingIt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxEditorBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}
	content, err := s.ReadFile("large.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !content.TooLarge || content.Content != "" || content.Size != maxEditorBytes+1 || content.Revision == "" {
		t.Fatalf("oversized content = %+v", content)
	}
}
