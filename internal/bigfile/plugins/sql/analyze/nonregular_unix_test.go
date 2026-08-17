//go:build !windows

package analyze

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"novera/internal/bigfile/regularfile"
)

func TestAnalyzeFileRejectsFIFOWithoutWaitingForWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dump.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := AnalyzeFile(context.Background(), path, Options{})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, regularfile.ErrNotRegular) {
			t.Fatalf("AnalyzeFile(FIFO) error = %v, want regularfile.ErrNotRegular", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AnalyzeFile blocked waiting for a FIFO writer")
	}
}
