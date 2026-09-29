// Package persistfile provides bounded reads and crash-resistant atomic writes
// for small application-owned configuration files.
package persistfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
)

// ErrUnsafePath marks a managed persistence path that resolves through a
// symbolic link/reparse point or a non-regular primary file.
var ErrUnsafePath = errors.New("persistent file path is unsafe")

// PublishedError reports a failure that occurred only after the replacement
// was atomically published. Callers that coordinate another store must not
// compensate by rolling that store back: doing so could leave the new file
// pointing at state they just deleted. The error still matters because the
// containing directory was not confirmed durable.
type PublishedError struct {
	Path string
	Err  error
}

func (e *PublishedError) Error() string {
	return fmt.Sprintf("persistent file %q was published but its directory could not be synced: %v", e.Path, e.Err)
}

func (e *PublishedError) Unwrap() error { return e.Err }

// IsPublished reports whether err means the destination replacement is already
// visible even though the final durability confirmation failed.
func IsPublished(err error) bool {
	var published *PublishedError
	return errors.As(err, &published)
}

// Read returns at most maxBytes from path and rejects a file that exceeds the
// acquisition limit before callers decode or amplify it into object graphs.
func Read(path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, errors.New("persistfile: negative read limit")
	}
	if maxBytes == math.MaxInt64 {
		return nil, errors.New("persistfile: read limit is too large to detect overflow safely")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %q is a symbolic link or reparse point", ErrUnsafePath, path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q is not a regular file", ErrUnsafePath, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: opened file %q is not regular", ErrUnsafePath, path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds the %d-byte safety limit", path, maxBytes)
	}
	return b, nil
}

// WriteAtomic publishes data through a unique same-directory temporary file.
// The staged bytes are synced and verified before rename, then the directory is
// synced where the platform supports it.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return fmt.Errorf("%w: managed directory %q is linked or not a directory", ErrUnsafePath, dir)
	}
	f, err := os.CreateTemp(dir, ".novera-persist-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		_ = os.Remove(tmp)
	}()
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	closed = true
	verify, err := Read(tmp, int64(len(data)))
	if err != nil {
		return fmt.Errorf("verify staged persistent file: %w", err)
	}
	if !bytes.Equal(verify, data) {
		return errors.New("verify staged persistent file: bytes differ")
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := d.Sync(); err != nil && runtime.GOOS != "windows" {
		return &PublishedError{Path: path, Err: err}
	}
	return nil
}
