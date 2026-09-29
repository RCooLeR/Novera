//go:build windows

package secret

import "golang.org/x/sys/windows"

// windows.Rename uses MoveFileEx with replacement semantics, matching Unix
// rename(2) for an atomic same-directory publication over an existing store.
func replaceSecretFile(oldPath, newPath string) error {
	return windows.Rename(oldPath, newPath)
}
