//go:build !windows

package secret

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNamedPipePrimaryIsRejectedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("FIFO creation unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readSecretStoreFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStoreUnsafePath) {
			t.Fatalf("read error = %v, want ErrStoreUnsafePath", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("secret-store read blocked on a named pipe")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO evidence changed: %v", err)
	}
}
