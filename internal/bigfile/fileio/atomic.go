package fileio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const defaultTempSuffix = ".quarry.tmp"

var (
	statPath           = os.Stat
	openPath           = os.OpenFile
	renamePath         = os.Rename
	removePath         = os.Remove
	chmodPath          = os.Chmod
	syncDirPath        = syncDirectory
	ErrExists          = errors.New("output file already exists")
	ErrTempExists      = errors.New("temporary output file already exists")
	ErrBackupExists    = errors.New("overwrite backup file already exists")
	ErrBackupRecovered = errors.New("orphaned overwrite backup recovered")
)

type AtomicWriteOptions struct {
	Mode       os.FileMode
	Overwrite  bool
	TempSuffix string
}

type AtomicWriteSummary struct {
	Path         string
	TempPath     string
	BytesWritten int64
	Overwritten  bool
}

// WriteFileAtomic writes data to an exclusive temp file and publishes it with a
// final rename. The default policy refuses to overwrite an existing output.
func WriteFileAtomic(path string, data []byte, opts AtomicWriteOptions) (AtomicWriteSummary, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return AtomicWriteSummary{}, errors.New("output path is required")
	}
	suffix := opts.TempSuffix
	if suffix == "" {
		suffix = defaultTempSuffix
	}
	mode := opts.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}

	summary := AtomicWriteSummary{
		Path:     path,
		TempPath: path + suffix,
	}
	backupPath := path + ".quarry.overwrite.bak"

	// Adjacent recovery artifacts are evidence, not authorization to mutate a
	// destination. A forged or stale backup must never be replayed merely
	// because an unrelated write selected the same path.
	if _, err := statPath(backupPath); err == nil {
		return summary, fmt.Errorf("%w: %s", ErrBackupExists, backupPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return summary, err
	}

	if _, err := statPath(summary.TempPath); err == nil {
		return summary, fmt.Errorf("%w: %s", ErrTempExists, summary.TempPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return summary, err
	}

	if info, err := statPath(path); err == nil {
		if !opts.Overwrite {
			return summary, fmt.Errorf("%w: %s", ErrExists, path)
		}
		summary.Overwritten = true
		if existingMode := info.Mode().Perm(); existingMode != 0 {
			mode = existingMode
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return summary, err
	}

	f, err := openPath(summary.TempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return summary, fmt.Errorf("%w: %s", ErrTempExists, summary.TempPath)
		}
		return summary, err
	}

	var writeErr error
	if len(data) > 0 {
		n, err := f.Write(data)
		summary.BytesWritten = int64(n)
		if err != nil {
			writeErr = err
		} else if n != len(data) {
			writeErr = errors.New("short write")
		}
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		_ = removePath(summary.TempPath)
		return summary, writeErr
	}
	if closeErr != nil {
		_ = removePath(summary.TempPath)
		return summary, closeErr
	}

	if !opts.Overwrite {
		// This final check narrows but cannot eliminate the create-between-stat-
		// and-rename race on platforms where rename replaces the destination.
		// Callers that need strict no-clobber semantics must publish to a unique
		// path or hold an external lock.
		if _, err := statPath(path); err == nil {
			_ = removePath(summary.TempPath)
			return summary, fmt.Errorf("%w: %s", ErrExists, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			_ = removePath(summary.TempPath)
			return summary, err
		}
	}
	backupMoved := false
	if opts.Overwrite && summary.Overwritten {
		if err := renamePath(path, backupPath); err != nil {
			_ = removePath(summary.TempPath)
			return summary, err
		}
		backupMoved = true
	}

	if err := renamePath(summary.TempPath, path); err != nil {
		if backupMoved {
			_ = renamePath(backupPath, path)
		}
		_ = removePath(summary.TempPath)
		return summary, err
	}
	if err := syncDirPath(path); err != nil {
		return summary, err
	}
	if backupMoved {
		if err := removePath(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return summary, err
		}
		if err := syncDirPath(path); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

// RecoverOverwriteBackup is retained as an inspection-only compatibility
// boundary. Adjacent unauthenticated artifacts cannot authorize replay.
func RecoverOverwriteBackup(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, errors.New("output path is required")
	}
	backupPath := path + ".quarry.overwrite.bak"
	if _, err := statPath(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if _, err := statPath(backupPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return false, fmt.Errorf("%w: recovery artifact requires explicit inspection: %s", ErrBackupExists, backupPath)
}

func syncDirectory(path string) error {
	dir := filepath.Dir(path)
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		if runtime.GOOS == "windows" {
			// Windows commonly rejects fsync on directory handles; durability here
			// relies on NTFS rename semantics plus the already-synced file data.
			return nil
		}
		return err
	}
	return nil
}
