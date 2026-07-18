package cachepath

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"novera/internal/bigfile/settings"
)

// SourcePath returns a stable Novera-owned cache path for side data derived from
// a source file. The cache payload still stores source metadata and must validate
// it before reuse.
func SourcePath(kind string, sourcePath string, extension string) (string, error) {
	root, err := settings.ConfigDir()
	if err != nil {
		return "", err
	}
	return sourcePathForRoot(root, kind, sourcePath, extension)
}

// LegacySourcePath returns the former cache location as read-only migration
// input. Callers must validate derived data before republishing it under the
// canonical Novera path.
func LegacySourcePath(kind string, sourcePath string, extension string) (string, bool, error) {
	root, available, err := settings.LegacyConfigDir()
	if err != nil || !available {
		return "", available, err
	}
	path, err := sourcePathForRoot(root, kind, sourcePath, extension)
	return path, true, err
}

func sourcePathForRoot(root string, kind string, sourcePath string, extension string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("cache root is empty")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" || kind == "." || kind == ".." || strings.ContainsAny(kind, `/\`) || kind != filepath.Base(kind) {
		return "", errors.New("cache kind must be a single path segment")
	}
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", errors.New("source path is empty")
	}
	extension = strings.TrimSpace(extension)
	if extension == "" {
		return "", errors.New("cache extension is empty")
	}
	if strings.ContainsAny(extension, `/\`) || extension == "." || extension == ".." {
		return "", errors.New("cache extension must be a file extension")
	}
	if !strings.HasPrefix(extension, ".") {
		extension = "." + extension
	}

	cleanSource, err := filepath.Abs(sourcePath)
	if err != nil {
		cleanSource = filepath.Clean(sourcePath)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(cleanSource)))
	return filepath.Join(root, "cache", kind, hex.EncodeToString(sum[:])+extension), nil
}
