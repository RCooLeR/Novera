// Package paths provides workspace-rooted path resolution with containment.
//
// Every relative path coming from the UI is resolved against the open
// workspace root using securejoin, which evaluates symlinks while guaranteeing
// the result stays inside the root. This closes the symlink-escape gaps that
// existed in the previous (Fyne) generation's path resolvers.
package paths

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	securejoin "github.com/cyphar/filepath-securejoin"
)

// ErrNoWorkspace is returned when a path is resolved before a workspace is open.
var ErrNoWorkspace = errors.New("no workspace is open")

// ErrEscapesWorkspace is returned when a relative path would resolve outside
// the workspace root.
var ErrEscapesWorkspace = errors.New("path escapes the workspace")

// ErrRootNotAbsolute is returned when Resolve is given a root that is not an
// absolute, cleaned path. Containment is defined relative to root, so a
// relative/unclean root would silently change where "inside the workspace"
// points; we refuse it rather than guess.
var ErrRootNotAbsolute = errors.New("workspace root must be an absolute path")

// Resolve joins rel onto root and returns an absolute path guaranteed to be
// contained within root. Symlinks are evaluated; any component that would point
// outside root is clamped, so the returned path can never escape.
func Resolve(root, rel string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", ErrNoWorkspace
	}
	// Self-enforce the precondition every caller's containment depends on:
	// root must be absolute and cleaned. (Today workspace.Open passes an
	// EvalSymlinks'd Abs root; this guards future callers.)
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return "", ErrRootNotAbsolute
	}
	// Normalise separators so callers can pass forward-slash UI paths on Windows.
	rel = filepath.FromSlash(strings.TrimSpace(rel))
	joined, err := securejoin.SecureJoin(root, rel)
	if err != nil {
		return "", err
	}
	return joined, nil
}

// Rel returns the slash-separated path of abs relative to root, or an error if
// abs is not contained within root. On Windows the containment test is
// case-insensitive (the filesystem is), since abs can arrive from external
// sources — e.g. filesystem-watch events — whose drive-letter / component
// casing differs from the stored root; the returned path preserves abs's real
// casing by slicing it rather than lower-casing.
func Rel(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err == nil && !escapesRoot(rel) {
		return filepath.ToSlash(rel), nil
	}
	if runtime.GOOS == "windows" {
		cleanRoot := filepath.Clean(root)
		cleanAbs := filepath.Clean(abs)
		lr := strings.ToLower(cleanRoot)
		la := strings.ToLower(cleanAbs)
		if la == lr {
			return ".", nil
		}
		if prefix := lr + string(filepath.Separator); strings.HasPrefix(la, prefix) {
			return filepath.ToSlash(cleanAbs[len(prefix):]), nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", ErrEscapesWorkspace
}

func escapesRoot(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
