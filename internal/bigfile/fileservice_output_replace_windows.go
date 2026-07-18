//go:build windows

package bigfile

import "golang.org/x/sys/windows"

func replaceServiceOutput(oldPath, newPath string) error {
	return windows.Rename(oldPath, newPath)
}
