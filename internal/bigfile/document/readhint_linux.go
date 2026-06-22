//go:build linux

package document

import (
	"os"

	"golang.org/x/sys/unix"
)

// applyReadHint advises the kernel that this file is read sequentially, widening
// readahead for the front-to-back streaming passes. Best-effort; errors ignored.
func applyReadHint(f *os.File, _ string) *os.File {
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_SEQUENTIAL)
	return f
}
