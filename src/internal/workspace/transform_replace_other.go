//go:build !windows

package workspace

import "os"

func replaceTransformFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
