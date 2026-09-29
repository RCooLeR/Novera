// Package regularfile opens untrusted pathnames for bounded, read-only use.
package regularfile

import (
	"errors"
	"fmt"
	"os"
)

var (
	ErrNotRegular  = errors.New("path does not identify a regular file")
	ErrPathChanged = errors.New("path changed while opening regular file")
)

func Open(path string) (*os.File, error) {
	return openRegular(path, false, nil)
}

func OpenNoFollow(path string) (*os.File, error) {
	return openRegular(path, true, nil)
}

func validateOpened(file *os.File, path string) (os.FileInfo, error) {
	if file == nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrInvalid}
	}
	info, err := file.Stat()
	if err != nil {
		return nil, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if !info.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return info, nil
}

func rejectPreflight(path string, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return nil
}

func pinPreflightIdentity(path string, info os.FileInfo) error {
	if info == nil || !os.SameFile(info, info) {
		return &os.PathError{Op: "stat", Path: path, Err: errors.Join(ErrPathChanged, ErrNotRegular)}
	}
	return nil
}

func rejectChanged(path string) error {
	return &os.PathError{Op: "open", Path: path, Err: ErrPathChanged}
}

func closeAfterError(file *os.File, err error) error {
	if file == nil {
		return err
	}
	if closeErr := file.Close(); closeErr != nil {
		return errors.Join(err, fmt.Errorf("close rejected regular-file handle: %w", closeErr))
	}
	return err
}
