package fileio

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SamePath reports whether two paths identify the same filesystem target under
// the current platform's path rules. It is case-insensitive on Windows, keeps
// case-sensitive spelling distinct on Unix-like systems, and uses os.SameFile
// when both paths already exist so hard links and symlinks are handled safely.
func SamePath(a string, b string) (bool, error) {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false, nil
	}

	cleanA, err := cleanAbsPath(a)
	if err != nil {
		return false, err
	}
	cleanB, err := cleanAbsPath(b)
	if err != nil {
		return false, err
	}
	if pathsEqualByPlatform(cleanA, cleanB) {
		return true, nil
	}

	infoA, errA := statPath(cleanA)
	infoB, errB := statPath(cleanB)
	if errA == nil && errB == nil {
		return os.SameFile(infoA, infoB), nil
	}
	if errA != nil && !errors.Is(errA, os.ErrNotExist) {
		return false, errA
	}
	if errB != nil && !errors.Is(errB, os.ErrNotExist) {
		return false, errB
	}
	return false, nil
}

func cleanAbsPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func pathsEqualByPlatform(a string, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
