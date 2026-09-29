package bigfile

import (
	"context"
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

var (
	ErrOutputAliasesSource      = errors.New("big-file output aliases opened source")
	ErrOutputSourceChanged      = errors.New("big-file source changed before output publication")
	ErrOutputValidationRequired = errors.New("big-file output validation callback is required")
)

type stagedServiceOutput struct {
	sourcePath  string
	sourceInfo  fs.FileInfo
	sourceState document.FileState
	sourceDoc   *document.FileDocument
	finalPath   string
	tempPath    string
	file        *os.File
	written     int64
	published   bool
}

// writeSafeOutput runs producer against an exclusive same-directory temporary
// file. The final path is replaced only after producer success, file sync, and
// close. Any earlier failure removes the partial temp and preserves an existing
// destination byte-for-byte.
func writeSafeOutput(doc *document.FileDocument, sourcePath, destination string, producer func(io.Writer) error) (int64, error) {
	return writeSafeOutputContext(context.Background(), doc, sourcePath, destination, producer)
}

func writeSafeOutputContext(ctx context.Context, doc *document.FileDocument, sourcePath, destination string, producer func(io.Writer) error) (int64, error) {
	return writeSafeOutputValidatedContext(ctx, doc, sourcePath, destination, producer, nil)
}

// writeSafeOutputValidated adds an operation-specific validation immediately
// before publication. The producer still writes only to an operation-owned
// same-directory temporary file, and every validation failure removes that
// temporary file while preserving source and destination bytes.
func writeSafeOutputValidated(doc *document.FileDocument, sourcePath, destination string, producer func(io.Writer) error, validate func() error) (int64, error) {
	return writeSafeOutputValidatedContext(context.Background(), doc, sourcePath, destination, producer, func(context.Context) error {
		if validate == nil {
			return nil
		}
		return validate()
	})
}

// writeSafeOutputValidatedContext binds the final publication point to the
// owning job context. Validation may be expensive and runs before the
// publication lock; only the final atomic replacement and durability step are
// serialized with cancellation.
func writeSafeOutputValidatedContext(ctx context.Context, doc *document.FileDocument, sourcePath, destination string, producer func(io.Writer) error, validate func(context.Context) error) (int64, error) {
	if doc == nil {
		return 0, errors.New("source document is required")
	}
	if producer == nil {
		return 0, errors.New("output producer is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	out, err := newStagedServiceOutput(doc, sourcePath, destination)
	if err != nil {
		return 0, err
	}
	defer out.abort()
	if err := producer(out); err != nil {
		return out.written, err
	}
	if err := out.commit(ctx, validate); err != nil {
		return out.written, err
	}
	return out.written, nil
}

func newStagedServiceOutput(doc *document.FileDocument, sourcePath, destination string) (*stagedServiceOutput, error) {
	if strings.TrimSpace(destination) == "" {
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
		sourcePath:  sourcePath,
		sourceInfo:  sourceInfo,
		sourceState: doc.OriginalFileState(),
		sourceDoc:   doc,
		finalPath:   destination,
		tempPath:    tempPath,
		file:        tmp,
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

func (o *stagedServiceOutput) commit(ctx context.Context, validate func(context.Context) error) error {
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
	if err := o.validateSource(); err != nil {
		return err
	}
	if validate != nil {
		if err := validate(ctx); err != nil {
			return fmt.Errorf("validate output source: %w", err)
		}
	}
	if err := rejectServiceOutputAlias(o.sourcePath, o.sourceInfo, o.finalPath); err != nil {
		return err
	}
	return publishServiceJobOutput(ctx, func() (bool, error) {
		if err := replaceServiceOutput(o.tempPath, o.finalPath); err != nil {
			return false, fmt.Errorf("publish staged output: %w", err)
		}
		o.published = true
		if err := syncServiceOutputDirectory(o.finalPath); err != nil {
			return true, fmt.Errorf("sync output directory: %w", err)
		}
		return true, nil
	})
}

func (o *stagedServiceOutput) validateSource() error {
	if o == nil || o.sourceDoc == nil || o.sourceInfo == nil {
		return ErrOutputValidationRequired
	}
	opened, err := o.sourceDoc.OpenedFileInfo()
	if err != nil {
		return fmt.Errorf("%w: inspect retained source: %v", ErrOutputSourceChanged, err)
	}
	current, err := os.Stat(o.sourcePath)
	if err != nil {
		return fmt.Errorf("%w: inspect current source path: %v", ErrOutputSourceChanged, err)
	}
	if !opened.Mode().IsRegular() || !current.Mode().IsRegular() ||
		!os.SameFile(o.sourceInfo, opened) || !os.SameFile(opened, current) ||
		opened.Size() != o.sourceInfo.Size() || !opened.ModTime().Equal(o.sourceInfo.ModTime()) ||
		current.Size() != o.sourceState.Size || !current.ModTime().Equal(o.sourceState.ModTime) {
		return ErrOutputSourceChanged
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
