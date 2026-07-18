package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrTransformOutputAliasesInput is returned when a data transform's output
// resolves to the same file as its input. Legacy transforms do not implement
// in-place semantics, so accepting this would replace or truncate the source.
var ErrTransformOutputAliasesInput = errors.New("transform output aliases its input")

// transformOutput stages a transform in the destination directory and only
// publishes it after the producer, file sync, and close have all succeeded.
// sourceInfo pins the identity of the already-open input, allowing hard-link
// aliases to be detected even if the source pathname changes during the run.
type transformOutput struct {
	sourcePath string
	sourceInfo fs.FileInfo
	finalPath  string
	tempPath   string
	file       *os.File
	notifier   SelfWriteNotifier
	published  bool
}

// newTransformOutput validates source/destination separation and creates an
// exclusive, same-directory temporary output. source must be the exact handle
// the transform will read so identity checks are tied to the opened input, not
// just to a pathname that can race.
func (s *Service) newTransformOutput(sourcePath string, source *os.File, outRel string) (*transformOutput, error) {
	if source == nil {
		return nil, errors.New("transform input is not open")
	}
	sourceInfo, err := source.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect transform input: %w", err)
	}
	outAbs, err := s.prepOutput(outRel)
	if err != nil {
		return nil, err
	}
	if err := rejectTransformOutputAlias(sourcePath, sourceInfo, outAbs); err != nil {
		return nil, err
	}

	var existingMode fs.FileMode
	preserveExistingMode := false
	if info, statErr := os.Stat(outAbs); statErr == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("transform output is a directory: %s", outRel)
		}
		existingMode = info.Mode().Perm()
		preserveExistingMode = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect transform output: %w", statErr)
	}

	tmp, err := os.CreateTemp(filepath.Dir(outAbs), ".novera-transform-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create staged transform output: %w", err)
	}
	tempPath := tmp.Name()
	// CreateTemp intentionally leaves new outputs private (0600, further
	// restricted by the process umask). Replacements retain the destination's
	// prior permission bits, including an explicit mode 000.
	if preserveExistingMode {
		err = tmp.Chmod(existingMode)
	}
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("set staged transform output permissions: %w", err)
	}
	return &transformOutput{
		sourcePath: sourcePath,
		sourceInfo: sourceInfo,
		finalPath:  outAbs,
		tempPath:   tempPath,
		file:       tmp,
		notifier:   s.selfWrite,
	}, nil
}

func (o *transformOutput) Write(p []byte) (int, error) {
	if o == nil || o.file == nil {
		return 0, errors.New("transform output is closed")
	}
	return o.file.Write(p)
}

// commit flushes and atomically publishes the staged file. The identity check
// is repeated immediately before the rename so a destination changed to a
// symlink or hard link while the transform was running is rejected as well.
func (o *transformOutput) commit() error {
	if o == nil || o.file == nil {
		return errors.New("transform output is closed")
	}
	if err := o.file.Sync(); err != nil {
		return fmt.Errorf("sync staged transform output: %w", err)
	}
	if err := o.file.Close(); err != nil {
		o.file = nil
		return fmt.Errorf("close staged transform output: %w", err)
	}
	o.file = nil

	if err := rejectTransformOutputAlias(o.sourcePath, o.sourceInfo, o.finalPath); err != nil {
		return err
	}
	if o.notifier != nil {
		o.notifier.Suppress(o.finalPath)
	}
	if err := replaceTransformFile(o.tempPath, o.finalPath); err != nil {
		return fmt.Errorf("publish transform output: %w", err)
	}
	o.published = true
	if err := syncTransformDirectory(o.finalPath); err != nil {
		return fmt.Errorf("sync transform output directory: %w", err)
	}
	return nil
}

// abort closes and removes an unpublished staged file. It is safe to defer
// immediately after newTransformOutput succeeds.
func (o *transformOutput) abort() {
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

func rejectTransformOutputAlias(sourcePath string, sourceInfo fs.FileInfo, outputPath string) error {
	if equalTransformPath(sourcePath, outputPath) {
		return transformAliasError(sourcePath, outputPath)
	}

	// EvalSymlinks adds an explicit canonical-path comparison. os.Stat below
	// follows the final symlink too, but retaining both checks covers platforms
	// whose FileInfo identity support is weaker than their path canonicalisation.
	resolvedSource, sourceErr := filepath.EvalSymlinks(sourcePath)
	resolvedOutput, outputErr := filepath.EvalSymlinks(outputPath)
	if sourceErr == nil && outputErr == nil && equalTransformPath(resolvedSource, resolvedOutput) {
		return transformAliasError(sourcePath, outputPath)
	}

	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect transform output identity: %w", err)
	}
	if sourceInfo != nil && os.SameFile(sourceInfo, outputInfo) {
		return transformAliasError(sourcePath, outputPath)
	}
	return nil
}

func equalTransformPath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func transformAliasError(sourcePath, outputPath string) error {
	return fmt.Errorf("%w: input %q and output %q identify the same file", ErrTransformOutputAliasesInput, sourcePath, outputPath)
}

func syncTransformDirectory(path string) error {
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
