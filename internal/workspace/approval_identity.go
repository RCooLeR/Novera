package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"novera/internal/paths"
)

const approvalContentHashLimit = 8 << 20

// ApprovalResourceIdentity is a bounded snapshot used to bind an approval to
// the state of the workspace resource the user reviewed. Small regular files
// use a content digest; large files and directories use an explicit metadata
// token so creating an approval never performs an unbounded read.
//
// This type is consumed by the Agent service through package functions. It is
// deliberately not exposed as a bound Service method.
type ApprovalResourceIdentity struct {
	Path       string `json:"path"`
	State      string `json:"state"`
	Revision   string `json:"revision"`
	LinkTarget string `json:"linkTarget,omitempty"`
	// HashedBytes is internal acquisition-budget accounting. It participates in
	// backend equality checks but is omitted from the canonical wire envelope.
	HashedBytes int64 `json:"-"`
}

// ApprovalWorkspaceIdentity captures the exact root generation under which an
// operation was reviewed. Package-level wiring avoids enlarging the generated
// frontend bridge surface.
func ApprovalWorkspaceIdentity(s *Service) (root string, generation uint64) {
	if s == nil {
		return "", 0
	}
	return s.workspaceSnapshot()
}

// CaptureApprovalResource returns a bounded identity for rel. Missing targets
// are represented explicitly because a destination changing from absent to
// present while the approval dialog is open is itself a stale approval.
func CaptureApprovalResource(s *Service, rel string) (ApprovalResourceIdentity, error) {
	identity := ApprovalResourceIdentity{Path: filepath.ToSlash(strings.TrimSpace(rel))}
	if s == nil {
		return identity, errors.New("workspace service is unavailable")
	}
	root, _ := s.workspaceSnapshot()
	if strings.TrimSpace(root) == "" {
		return identity, errors.New("no workspace is open")
	}
	abs, err := paths.Resolve(root, rel)
	if err != nil {
		return identity, err
	}
	if err := assertContainedReal(root, abs); err != nil {
		return identity, err
	}

	lstat, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		identity.State = "missing"
		identity.Revision = "missing"
		return identity, nil
	}
	if err != nil {
		return identity, err
	}

	stat := lstat
	if lstat.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return identity, fmt.Errorf("resolve approval resource %q: %w", rel, err)
		}
		resolvedRel, err := paths.Rel(root, resolved)
		if err != nil {
			return identity, paths.ErrEscapesWorkspace
		}
		identity.LinkTarget = filepath.ToSlash(resolvedRel)
		stat, err = os.Stat(abs)
		if err != nil {
			return identity, err
		}
	}

	switch {
	case stat.Mode().IsRegular() && stat.Size() <= approvalContentHashLimit:
		f, err := os.Open(abs)
		if err != nil {
			return identity, err
		}
		h := sha256.New()
		_, copyErr := io.CopyN(h, f, approvalContentHashLimit+1)
		closeErr := f.Close()
		if copyErr == nil {
			return identity, fmt.Errorf("approval resource %q changed while it was being captured", rel)
		}
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			return identity, copyErr
		}
		if closeErr != nil {
			return identity, closeErr
		}
		identity.State = "file-sha256"
		identity.Revision = hex.EncodeToString(h.Sum(nil))
		identity.HashedBytes = stat.Size()
	case stat.Mode().IsRegular():
		identity.State = "file-metadata"
		identity.Revision = approvalMetadataRevision(stat)
	case stat.IsDir():
		identity.State = "directory-metadata"
		identity.Revision = approvalMetadataRevision(stat)
	default:
		identity.State = "other"
		identity.Revision = approvalMetadataRevision(stat)
	}
	return identity, nil
}

func approvalMetadataRevision(info fs.FileInfo) string {
	// UnixNano is stable for comparisons within one running process and avoids
	// locale/time-zone representations. Mode and size distinguish common
	// substitutions even on filesystems with coarse timestamp precision.
	return fmt.Sprintf("mode=%s;size=%d;mtime=%d", info.Mode(), info.Size(), info.ModTime().UTC().UnixNano())
}
