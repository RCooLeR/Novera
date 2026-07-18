//go:build !windows

package main

import (
	"fmt"
	"os"
)

func replaceExportFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func syncExportDirectory(dir, publishedPath string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("export %q was published but its directory could not be opened for sync: %w", publishedPath, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("export %q was published but its directory could not be synced: %w", publishedPath, err)
	}
	return nil
}
