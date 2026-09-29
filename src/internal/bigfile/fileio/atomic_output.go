package fileio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	ErrSourceAlias       = errors.New("output aliases a source file")
	ErrOutputNotOpen     = errors.New("atomic output is not open")
	ErrOutputAlreadyDone = errors.New("atomic output is already published")
	ErrInvalidExactPath  = errors.New("output path spelling is not exact")
)

// PublicationBoundary serializes final-name publication with an operation
// owner's cancellation decision. Service jobs install this boundary in their
// context; lower-level streaming packages only need to propagate that context.
type PublicationBoundary func(publish func() error) error

type publicationBoundaryContextKey struct{}

// WithPublicationBoundary installs the first authoritative publication
// boundary. Nested engines cannot replace an outer owner with a weaker one.
func WithPublicationBoundary(ctx context.Context, boundary PublicationBoundary) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if boundary == nil {
		return ctx
	}
	if existing, _ := ctx.Value(publicationBoundaryContextKey{}).(PublicationBoundary); existing != nil {
		return ctx
	}
	return context.WithValue(ctx, publicationBoundaryContextKey{}, boundary)
}

func publishWithinBoundary(ctx context.Context, publish func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	boundary, _ := ctx.Value(publicationBoundaryContextKey{}).(PublicationBoundary)
	if boundary == nil {
		return publish()
	}
	return boundary(publish)
}

// InvalidExactPathError reports an output spelling that filepath.Clean would
// silently rewrite. Output publication rejects such paths before deriving its
// parent or temporary name.
type InvalidExactPathError struct {
	Path      string
	CleanPath string
	Reason    string
}

func (e *InvalidExactPathError) Error() string {
	if e == nil {
		return ErrInvalidExactPath.Error()
	}
	if e.Reason != "" {
		return fmt.Sprintf("%v %q: %s", ErrInvalidExactPath, e.Path, e.Reason)
	}
	return fmt.Sprintf("%v %q (clean spelling would be %q)", ErrInvalidExactPath, e.Path, e.CleanPath)
}

func (e *InvalidExactPathError) Unwrap() error { return ErrInvalidExactPath }

// PublicationError reports a failure after a complete output became visible.
// Callers must not treat this state as an ordinary no-output failure.
type PublicationError struct {
	FinalPath         string
	Durable           bool
	LocationUncertain bool
	Err               error
}

func (e *PublicationError) Error() string {
	if e == nil {
		return "output publication failed"
	}
	if e.LocationUncertain {
		return fmt.Sprintf("publication state for %q is uncertain: %v", e.FinalPath, e.Err)
	}
	if e.Durable {
		return fmt.Sprintf("output exists at %q, but finalization failed: %v", e.FinalPath, e.Err)
	}
	return fmt.Sprintf("output exists at %q, but durability is unconfirmed: %v", e.FinalPath, e.Err)
}

func (e *PublicationError) Unwrap() error { return e.Err }

// beforeAtomicOutputPublish is a test seam at the final no-clobber publication
// boundary.
var beforeAtomicOutputPublish = func() {}

// AtomicOutput writes to an operation-owned, same-directory temporary file.
// Commit publishes the complete file with an atomic hard-link operation, which
// refuses an existing destination. Filesystems that cannot create hard links
// fail closed and leave no partial file at the final name.
//
// This compact compatibility primitive is path-bound, unlike Quarry's
// platform-specific handle-relative implementation. It verifies the retained
// file identity immediately before and after publication, but cannot eliminate
// a hostile temporary-path or parent-directory substitution race.
type AtomicOutput struct {
	finalPath string
	tempPath  string
	file      *os.File
	ownedInfo os.FileInfo
	published bool
}

// ValidateExactOutputPath rejects a path whose spelling would be changed by
// lexical cleaning.
func ValidateExactOutputPath(path string) error {
	if path == "" {
		return errors.New("output path is required")
	}
	clean := filepath.Clean(path)
	if clean != path {
		return &InvalidExactPathError{Path: path, CleanPath: clean}
	}
	_, base := filepath.Split(path)
	if base == "" || base == "." || base == ".." {
		return &InvalidExactPathError{Path: path, CleanPath: clean, Reason: "path must name a file"}
	}
	return validatePlatformExactPath(path, clean)
}

// ValidateExactDirectoryPath applies the same no-rewrite contract to a parent
// directory. Dot-only directories are rejected because appending a child with
// filepath.Join would otherwise erase the caller's selected spelling.
func ValidateExactDirectoryPath(path string) error {
	clean := filepath.Clean(path)
	if path == "" || clean != path {
		return &InvalidExactPathError{Path: path, CleanPath: clean}
	}
	if path == "." || path == ".." {
		return &InvalidExactPathError{
			Path:      path,
			CleanPath: clean,
			Reason:    "directory must not be a dot-only spelling",
		}
	}
	return validatePlatformExactPath(path, clean)
}

// ExactChildPath constructs one direct child without filepath.Join cleaning
// the selected parent. The child name must already be one filename component.
func ExactChildPath(dir string, base string) (string, error) {
	if err := ValidateExactDirectoryPath(dir); err != nil {
		return "", err
	}
	baseDir, baseName := filepath.Split(base)
	if base == "" || baseDir != "" || baseName != base || base == "." || base == ".." || filepath.VolumeName(base) != "" {
		return "", &InvalidExactPathError{
			Path:      base,
			CleanPath: filepath.Clean(base),
			Reason:    "child name must be one non-dot filename component",
		}
	}
	child := dir
	if child[len(child)-1] != byte(filepath.Separator) {
		child += string(filepath.Separator)
	}
	child += base
	if err := ValidateExactOutputPath(child); err != nil {
		return "", err
	}
	return child, nil
}

// OpenAtomicOutput rejects source aliases and existing destinations before
// creating a private temporary file in the destination directory.
func OpenAtomicOutput(finalPath string, sourcePaths []string, mode os.FileMode) (_ *AtomicOutput, retErr error) {
	if err := ValidateExactOutputPath(finalPath); err != nil {
		return nil, err
	}
	for _, sourcePath := range sourcePaths {
		same, err := SamePath(sourcePath, finalPath)
		if err != nil {
			return nil, err
		}
		if same {
			return nil, fmt.Errorf("%w: %s", ErrSourceAlias, finalPath)
		}
	}
	if _, err := os.Lstat(finalPath); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrExists, finalPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	mode = mode.Perm()
	if mode == 0 {
		mode = 0o600
	}
	dir, base := filepath.Split(finalPath)
	if dir == "" {
		dir = "."
	}
	file, err := os.CreateTemp(dir, "."+base+".novera-*")
	if err != nil {
		return nil, err
	}
	out := &AtomicOutput{
		finalPath: finalPath,
		tempPath:  file.Name(),
		file:      file,
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, out.Cleanup())
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("temporary output %q is not a regular file", out.tempPath)
	}
	out.ownedInfo = info
	return out, nil
}

func (o *AtomicOutput) Path() string {
	if o == nil {
		return ""
	}
	return o.finalPath
}

func (o *AtomicOutput) TempPath() string {
	if o == nil {
		return ""
	}
	return o.tempPath
}

func (o *AtomicOutput) Write(p []byte) (int, error) {
	if o == nil || o.file == nil || o.published {
		return 0, ErrOutputNotOpen
	}
	return o.file.Write(p)
}

func (o *AtomicOutput) Sync() error {
	if o == nil || o.file == nil || o.published {
		return ErrOutputNotOpen
	}
	return o.file.Sync()
}

func (o *AtomicOutput) Commit() error {
	return o.CommitContext(context.Background())
}

// CommitContext syncs all output bytes, observes cancellation, and atomically
// publishes without replacing an existing final entry.
func (o *AtomicOutput) CommitContext(ctx context.Context) error {
	return o.commitContext(ctx, nil)
}

// CommitContextValidated syncs the complete output, invokes validate, checks
// cancellation again, and then immediately enters the publication primitive.
// Validation and publication affect different filesystem objects and cannot be
// one atomic operation; this deliberately narrows, but cannot eliminate, that
// race.
func (o *AtomicOutput) CommitContextValidated(ctx context.Context, validate func(context.Context) error) error {
	if validate == nil {
		return errors.New("atomic output publication validator is required")
	}
	return o.commitContext(ctx, validate)
}

func (o *AtomicOutput) commitContext(ctx context.Context, validate func(context.Context) error) error {
	if o == nil || o.published {
		return ErrOutputAlreadyDone
	}
	if o.file == nil {
		return ErrOutputNotOpen
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.file.Sync(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.verifyOwnedTemp(); err != nil {
		return err
	}
	if validate != nil {
		if err := validate(ctx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	return publishWithinBoundary(ctx, func() error {
		beforeAtomicOutputPublish()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Link(o.tempPath, o.finalPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("%w: %s", ErrExists, o.finalPath)
			}
			return fmt.Errorf("publish complete output without clobber: %w", err)
		}
		o.published = true

		finalInfo, err := os.Stat(o.finalPath)
		if err != nil || !os.SameFile(o.ownedInfo, finalInfo) {
			if err == nil {
				err = errors.New("final path resolves to a different file")
			}
			return &PublicationError{
				FinalPath:         o.finalPath,
				LocationUncertain: true,
				Err:               err,
			}
		}
		if err := syncDirPath(o.finalPath); err != nil {
			return &PublicationError{FinalPath: o.finalPath, Err: err}
		}
		if err := o.closeAndRemoveOwnedTemp(); err != nil {
			return &PublicationError{FinalPath: o.finalPath, Durable: true, Err: err}
		}
		return nil
	})
}

// Cleanup closes and removes only the operation-owned temporary identity. It
// never removes the final output and refuses to unlink a substituted temp path.
func (o *AtomicOutput) Cleanup() error {
	if o == nil {
		return nil
	}
	return o.closeAndRemoveOwnedTemp()
}

func (o *AtomicOutput) verifyOwnedTemp() error {
	if o == nil || o.tempPath == "" || o.ownedInfo == nil {
		return nil
	}
	info, err := os.Stat(o.tempPath)
	if err != nil {
		return err
	}
	if !os.SameFile(o.ownedInfo, info) {
		return fmt.Errorf("temporary output path %q no longer identifies the operation-owned file", o.tempPath)
	}
	return nil
}

func (o *AtomicOutput) closeAndRemoveOwnedTemp() error {
	if o == nil {
		return nil
	}
	var closeErr error
	if o.file != nil {
		closeErr = o.file.Close()
		o.file = nil
	}
	if o.tempPath == "" {
		return closeErr
	}
	info, statErr := os.Stat(o.tempPath)
	if errors.Is(statErr, os.ErrNotExist) {
		o.tempPath = ""
		return closeErr
	}
	if statErr != nil {
		return errors.Join(closeErr, statErr)
	}
	if o.ownedInfo == nil || !os.SameFile(o.ownedInfo, info) {
		return errors.Join(closeErr, fmt.Errorf("refuse to remove substituted temporary output %q", o.tempPath))
	}
	if err := os.Remove(o.tempPath); err != nil {
		return errors.Join(closeErr, err)
	}
	o.tempPath = ""
	return errors.Join(closeErr, syncDirPath(o.finalPath))
}
