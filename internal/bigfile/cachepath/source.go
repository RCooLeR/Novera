package cachepath

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"novera/internal/bigfile/settings"
)

// SourcePath returns a stable Quarry-owned cache path for side data derived from
// a source file. The cache payload still stores source metadata and must validate
// it before reuse.
func SourcePath(kind string, sourcePath string, extension string) (string, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" || kind != filepath.Base(kind) {
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
	if !strings.HasPrefix(extension, ".") {
		extension = "." + extension
	}

	cleanSource, err := filepath.Abs(sourcePath)
	if err != nil {
		cleanSource = filepath.Clean(sourcePath)
	}
	root, err := settings.ConfigDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(cleanSource)))
	return filepath.Join(root, "cache", kind, hex.EncodeToString(sum[:])+extension), nil
}
