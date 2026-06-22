package fileio

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSamePathUsesPlatformCaseRules(t *testing.T) {
	dir := t.TempDir()
	lower := filepath.Join(dir, "source.txt")
	upper := filepath.Join(dir, "SOURCE.txt")

	got, err := SamePath(lower, upper)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if !got {
			t.Fatal("SamePath = false on Windows for case-only path difference")
		}
		return
	}
	if got {
		t.Fatal("SamePath = true on case-sensitive platform for case-only path difference")
	}
}

func TestSamePathRecognizesExistingSameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	same, err := SamePath(path, filepath.Join(dir, ".", "source.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("SamePath = false for equivalent cleaned paths")
	}
}

func TestSamePathRecognizesHardLinkWhenSupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard-link permission depends on Windows policy")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	link := filepath.Join(dir, "linked.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, link); err != nil {
		t.Skipf("hard links not supported here: %v", err)
	}

	same, err := SamePath(path, link)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("SamePath = false for hard-linked files")
	}
}
