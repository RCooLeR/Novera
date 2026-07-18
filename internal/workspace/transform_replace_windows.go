//go:build windows

package workspace

import "golang.org/x/sys/windows"

// windows.Rename uses MoveFileEx with MOVEFILE_REPLACE_EXISTING, giving the
// staged transform the same atomic replace behavior as rename(2) on Unix.
func replaceTransformFile(oldPath, newPath string) error {
	return windows.Rename(oldPath, newPath)
}
