package fileio

import "os"

func Stat(path string) (os.FileInfo, error) {
	return statPath(path)
}

func Rename(oldPath string, newPath string) error {
	return renamePath(oldPath, newPath)
}

func Remove(path string) error {
	return removePath(path)
}
