//go:build !windows

package regularfile

import (
	"os"

	"golang.org/x/sys/unix"
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

	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK
	if noFollow {
		flags |= unix.O_NOFOLLOW
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
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

	flags, err = unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return nil, closeAfterError(file, &os.PathError{Op: "fcntl", Path: path, Err: err})
	}
	if flags&unix.O_NONBLOCK != 0 {
		if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFL, flags&^unix.O_NONBLOCK); err != nil {
			return nil, closeAfterError(file, &os.PathError{Op: "fcntl", Path: path, Err: err})
		}
	}
	return file, nil
}
