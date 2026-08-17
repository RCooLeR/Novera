package regularfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAcceptsRegularFileAndRejectsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("regular"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	file, err = Open(t.TempDir())
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Open(directory) error = %v, want ErrNotRegular", err)
	}
}

func TestOpenRejectsRegularFileExchangedAfterPreflight(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	oldPath := filepath.Join(dir, "source.old")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openRegular(path, false, func() error {
		if err := os.Rename(path, oldPath); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("second"), 0o600)
	})
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, ErrPathChanged) {
		t.Fatalf("open exchanged regular file error = %v, want ErrPathChanged", err)
	}
}
