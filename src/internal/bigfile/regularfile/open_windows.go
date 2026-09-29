//go:build windows

package regularfile

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func openRegular(path string, noFollow bool, afterPreflight func() error) (*os.File, error) {
	statPath := os.Stat
	if noFollow {
		statPath = os.Lstat
	}
	preflight, err := statPath(path)
	if err != nil {
		return nil, err
	}
	if err := rejectPreflight(path, preflight); err != nil {
		return nil, err
	}
	if err := pinPreflightIdentity(path, preflight); err != nil {
		return nil, err
	}
	if afterPreflight != nil {
		if err := afterPreflight(); err != nil {
			return nil, err
		}
	}

	openPath, err := windowsPath(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	name, err := windows.UTF16PtrFromString(openPath)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	flags := uint32(windows.FILE_FLAG_BACKUP_SEMANTICS | windows.FILE_FLAG_SEQUENTIAL_SCAN)
	if noFollow {
		flags |= windows.FILE_FLAG_OPEN_REPARSE_POINT
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if fileType != windows.FILE_TYPE_DISK {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &handleInfo); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 ||
		(noFollow && handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0) {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}

	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrInvalid}
	}
	opened, err := validateOpened(file, path)
	if err != nil {
		return nil, closeAfterError(file, err)
	}
	if !os.SameFile(preflight, opened) {
		return nil, closeAfterError(file, rejectChanged(path))
	}
	current, err := statPath(path)
	if err != nil {
		return nil, closeAfterError(file, &os.PathError{Op: "stat", Path: path, Err: err})
	}
	if err := rejectPreflight(path, current); err != nil {
		return nil, closeAfterError(file, err)
	}
	if !os.SameFile(opened, current) {
		return nil, closeAfterError(file, rejectChanged(path))
	}
	return file, nil
}

func windowsPath(path string) (string, error) {
	if len(path) < 248 || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) || strings.HasPrefix(path, `\\.\`) {
		return path, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(abs, `\\`), nil
	}
	return `\\?\` + abs, nil
}
