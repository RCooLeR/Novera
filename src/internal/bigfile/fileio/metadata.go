package fileio

import (
	"os"
)

// ApplyMode copies permission bits onto a path after safe-output promotion.
// It intentionally ignores type bits so callers cannot accidentally turn a
// regular output into a special file.
func ApplyMode(path string, mode os.FileMode) error {
	mode = mode.Perm()
	if mode == 0 {
		return nil
	}
	return chmodPath(path, mode)
}
