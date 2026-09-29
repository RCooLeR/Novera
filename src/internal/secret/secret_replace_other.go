//go:build !windows

package secret

import "os"

func replaceSecretFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
