//go:build windows

package artifacts

import "golang.org/x/sys/windows"

// registryPathLinkStatus checks Windows reparse metadata directly. Calling
// filepath.EvalSymlinks on an ordinary private metadata directory can fail with
// ERROR_ACCESS_DENIED under race-instrumented processes, while file attributes
// are both sufficient for this boundary and work without opening a directory
// handle.
func registryPathLinkStatus(path string) (resolved string, linked bool, err error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false, err
	}
	attributes, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return "", false, err
	}
	return path, attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
