//go:build !windows

package artifacts

import "path/filepath"

func registryPathLinkStatus(path string) (resolved string, linked bool, err error) {
	resolved, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", false, err
	}
	return resolved, !sameRegistryPath(path, resolved), nil
}
