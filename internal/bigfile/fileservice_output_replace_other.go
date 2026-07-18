//go:build !windows

package bigfile

import "os"

func replaceServiceOutput(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
