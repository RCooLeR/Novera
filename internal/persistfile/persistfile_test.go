package persistfile

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestReadIsBoundedAndWriteAtomicReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	first := []byte(`{"version":1}`)
	second := []byte(`{"version":2,"value":"updated"}`)
	if err := WriteAtomic(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, second, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path, int64(len(second)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, second) {
		t.Fatalf("persisted bytes = %q, want %q", got, second)
	}
	if _, err := Read(path, int64(len(second)-1)); err == nil {
		t.Fatal("oversized persistent file was accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(path) {
			t.Fatalf("temporary file was left behind: %s", entry.Name())
		}
	}
}

func TestReadRejectsNegativeLimit(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "missing"), -1)
	if err == nil {
		t.Fatalf("Read error = %v, want negative-limit error", err)
	}
}

func TestReadRejectsLimitThatCannotReserveOverflowByte(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "missing"), math.MaxInt64)
	if err == nil {
		t.Fatal("Read accepted a limit whose overflow sentinel would wrap")
	}
}

func TestPublishedErrorClassification(t *testing.T) {
	cause := errors.New("directory sync failed")
	err := &PublishedError{Path: "settings.json", Err: cause}
	if !IsPublished(err) || !errors.Is(err, cause) {
		t.Fatalf("published error classification failed: %v", err)
	}
	if IsPublished(cause) {
		t.Fatal("ordinary pre-publication error was classified as published")
	}
}

func TestRejectsLinkedPrimaryAndManagedDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte(`{"outside":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedFile := filepath.Join(root, "linked.json")
	if err := os.Symlink(target, linkedFile); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	if _, err := Read(linkedFile, 1024); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Read linked primary error = %v, want ErrUnsafePath", err)
	}

	outsideDir := filepath.Join(root, "outside")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "managed")
	if err := os.Symlink(outsideDir, linkedDir); err != nil {
		t.Skipf("directory symlinks unavailable on this host: %v", err)
	}
	if err := WriteAtomic(filepath.Join(linkedDir, "settings.json"), []byte(`{}`), 0o600); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("WriteAtomic linked directory error = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("linked-directory write escaped managed location: %v", err)
	}
}
