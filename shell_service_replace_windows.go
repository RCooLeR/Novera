//go:build windows

package main

import "golang.org/x/sys/windows"

func replaceExportFile(oldPath, newPath string) error {
	return windows.Rename(oldPath, newPath)
}

// Windows does not provide the same portable directory-fsync contract as Unix.
func syncExportDirectory(_, _ string) error { return nil }
