package fileio

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type ExclusiveOutput struct {
	path   string
	file   *os.File
	closed bool
}

func OpenExclusiveOutput(path string, mode os.FileMode) (*ExclusiveOutput, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("output path is required")
	}
	mode = mode.Perm()
	if mode == 0 {
		mode = 0o600
	}
	file, err := openPath(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrExists, path)
		}
		return nil, err
	}
	return &ExclusiveOutput{path: path, file: file}, nil
}

func (o *ExclusiveOutput) Path() string {
	if o == nil {
		return ""
	}
	return o.path
}

func (o *ExclusiveOutput) Write(p []byte) (int, error) {
	if o == nil || o.file == nil {
		return 0, errors.New("output is not open")
	}
	return o.file.Write(p)
}

func (o *ExclusiveOutput) Sync() error {
	if o == nil || o.file == nil || o.closed {
		return nil
	}
	return o.file.Sync()
}

func (o *ExclusiveOutput) Close() error {
	if o == nil || o.file == nil || o.closed {
		return nil
	}
	o.closed = true
	closeErr := o.file.Close()
	o.file = nil
	syncErr := syncDirPath(o.path)
	if closeErr != nil && syncErr != nil {
		return errors.Join(closeErr, syncErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return nil
}

func (o *ExclusiveOutput) Cleanup() error {
	if o == nil {
		return nil
	}
	closeErr := o.Close()
	removeErr := removePath(o.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	var syncErr error
	if removeErr == nil {
		syncErr = syncDirPath(o.path)
	}
	if closeErr != nil && removeErr != nil {
		return errors.Join(closeErr, removeErr, syncErr)
	}
	if closeErr != nil {
		if syncErr != nil {
			return errors.Join(closeErr, syncErr)
		}
		return closeErr
	}
	if removeErr != nil {
		return fmt.Errorf("remove output %q: %w", o.path, removeErr)
	}
	if syncErr != nil {
		return syncErr
	}
	return nil
}
