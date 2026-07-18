package bigfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/fileio"
)

var ErrOutputAliasesSource = errors.New("big-file output aliases opened source")

type stagedServiceOutput struct {
	sourcePath string
	sourceInfo fs.FileInfo
	finalPath  string
	tempPath   string
	file       *os.File
	written    int64
	published  bool
}

// writeSafeOutput runs producer against an exclusive same-directory temporary
// file. The final path is replaced only after producer success, file sync, and
// close. Any earlier failure removes the partial temp and preserves an existing
// destination byte-for-byte.
func writeSafeOutput(doc *document.FileDocument, sourcePath, destination string, producer func(io.Writer) error) (int64, error) {
	if doc == nil {
		return 0, errors.New("source document is required")
	}
	if producer == nil {
		return 0, errors.New("output producer is required")
	}
	out, err := newStagedServiceOutput(doc, sourcePath, destination)
	if err != nil {
		return 0, err
	}
	defer out.abort()
	if err := producer(out); err != nil {
		return out.written, err
	}
	if err := out.commit(); err != nil {
		return out.written, err
	}
	return out.written, nil
}

func newStagedServiceOutput(doc *document.FileDocument, sourcePath, destination string) (*stagedServiceOutput, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return nil, errors.New("output path is required")
	}
	sourceInfo, err := doc.OpenedFileInfo()
	if err != nil {
		return nil, fmt.Errorf("inspect opened source: %w", err)
	}
	if err := rejectServiceOutputAlias(sourcePath, sourceInfo, destination); err != nil {
		return nil, err
	}

	mode := fs.FileMode(0o600)
	if info, statErr := os.Stat(destination); statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("output is not a regular file: %s", destination)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect output: %w", statErr)
	}

	tmp, err := os.CreateTemp(filepath.Dir(destination), ".novera-bigfile-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create staged output: %w", err)
	}
	tempPath := tmp.Name()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("set staged output permissions: %w", err)
	}
	return &stagedServiceOutput{
		sourcePath: sourcePath,
		sourceInfo: sourceInfo,
		finalPath:  destination,
		tempPath:   tempPath,
		file:       tmp,
	}, nil
}

func (o *stagedServiceOutput) Write(p []byte) (int, error) {
	if o == nil || o.file == nil {
		return 0, errors.New("staged output is closed")
	}
	n, err := o.file.Write(p)
	o.written += int64(n)
	return n, err
}

func (o *stagedServiceOutput) commit() error {
	if o == nil || o.file == nil {
		return errors.New("staged output is closed")
	}
	if err := o.file.Sync(); err != nil {
		return fmt.Errorf("sync staged output: %w", err)
	}
	if err := o.file.Close(); err != nil {
		o.file = nil
		return fmt.Errorf("close staged output: %w", err)
	}
	o.file = nil
	if err := rejectServiceOutputAlias(o.sourcePath, o.sourceInfo, o.finalPath); err != nil {
		return err
	}
	if err := replaceServiceOutput(o.tempPath, o.finalPath); err != nil {
		return fmt.Errorf("publish staged output: %w", err)
	}
	o.published = true
	if err := syncServiceOutputDirectory(o.finalPath); err != nil {
		return fmt.Errorf("sync output directory: %w", err)
	}
	return nil
}

func (o *stagedServiceOutput) abort() {
	if o == nil {
		return
	}
	if o.file != nil {
		_ = o.file.Close()
		o.file = nil
	}
	if !o.published && o.tempPath != "" {
		_ = os.Remove(o.tempPath)
	}
}

func rejectServiceOutputAlias(sourcePath string, sourceInfo fs.FileInfo, destination string) error {
	if same, err := fileio.SamePath(sourcePath, destination); err != nil {
		return fmt.Errorf("compare source and output: %w", err)
	} else if same {
		return fmt.Errorf("%w: %q", ErrOutputAliasesSource, destination)
	}
	info, err := os.Stat(destination)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect output identity: %w", err)
	}
	if sourceInfo != nil && os.SameFile(sourceInfo, info) {
		return fmt.Errorf("%w: %q", ErrOutputAliasesSource, destination)
	}
	return nil
}

func syncServiceOutputDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}
