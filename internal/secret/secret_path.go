package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// inspectSecretStorePath rejects path types that can redirect a managed
// credential operation or make a bounded read block indefinitely. Missing
// paths are allowed because first-run creation is expected.
func inspectSecretStorePath(path string) error {
	dir := filepath.Dir(path)
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect secret-store directory %q: %w", dir, err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: managed directory %q is not a direct directory", ErrStoreUnsafePath, dir)
	}
	reparse, err := secretPathIsReparsePoint(dir)
	if err != nil {
		return fmt.Errorf("inspect secret-store directory %q: %w", dir, err)
	}
	if reparse {
		return fmt.Errorf("%w: managed directory %q is a reparse point", ErrStoreUnsafePath, dir)
	}

	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect encrypted secret store %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: primary %q is not a regular file", ErrStoreUnsafePath, path)
	}
	reparse, err = secretPathIsReparsePoint(path)
	if err != nil {
		return fmt.Errorf("inspect encrypted secret store %q: %w", path, err)
	}
	if reparse {
		return fmt.Errorf("%w: primary %q is a reparse point", ErrStoreUnsafePath, path)
	}
	return nil
}
