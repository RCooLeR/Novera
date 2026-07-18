// Package inplace applies length-preserving byte patches directly to a file,
// crash-safely. It is the fast save path for huge files: when staged edits do
// not change the file's total byte length, only the changed extents are
// overwritten (O(edits)) instead of rewriting the whole file (O(size)).
//
// Crash safety uses a reverse-patch sidecar (a write-ahead log of the original
// bytes) written and fsynced BEFORE the source is touched:
//
//  1. write sidecar (committed=0) with the original bytes of every extent;
//     fsync the file and its parent directory
//  2. overwrite each extent in the source with the new bytes; fsync source
//  3. mark the sidecar committed=1; fsync; remove it and sync its directory
//
// Recovery is deliberately explicit. A caller that has authenticated a
// leftover sidecar and confirmed it belongs to the target may invoke Recover:
//   - committed -> the patch finished; just delete the sidecar
//   - not committed -> the patch may be partial; replay the original bytes to
//     roll the source back to its pre-patch state, then delete the sidecar
//   - unparseable/partial -> fail closed and preserve the sidecar as recovery
//     evidence; the source is never opened for write
package inplace

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Patch overwrites [Offset, Offset+len(Old)) with New. New and Old must have
// the same length (the operation is length-preserving). Old is the expected
// current content; it is recorded for rollback and verified before writing.
type Patch struct {
	Offset int64
	Old    []byte
	New    []byte
}

var (
	// ErrNotLengthPreserving means a patch would change the byte length.
	ErrNotLengthPreserving = errors.New("inplace: patch is not length-preserving")
	// ErrDrift means the file's current bytes differ from Patch.Old.
	ErrDrift = errors.New("inplace: file content changed under the patch")
	// ErrOutOfRange means a patch extends beyond the file.
	ErrOutOfRange = errors.New("inplace: patch is out of range")
	// ErrOverlap means two patches overlap.
	ErrOverlap = errors.New("inplace: patches overlap")
	// ErrRecoveryPending means the recovery sidecar already exists. Apply must
	// never replace it because it may be the only evidence needed to recover an
	// interrupted earlier save.
	ErrRecoveryPending = errors.New("inplace: recovery sidecar already exists")
	// ErrRecoveryTooLarge means the rollback journal exceeds the bounded
	// representation this package is willing to create or parse.
	ErrRecoveryTooLarge = errors.New("inplace: recovery sidecar exceeds safety limit")
	// ErrInvalidSidecar means recovery evidence is malformed or internally
	// inconsistent. Recover preserves such evidence and leaves the source alone.
	ErrInvalidSidecar = errors.New("inplace: invalid recovery sidecar")
	// ErrRecoverySourceMismatch means the journal's recorded source size no
	// longer matches the file selected for recovery.
	ErrRecoverySourceMismatch = errors.New("inplace: recovery source does not match sidecar")
	// ErrRecoveryPathAlias means the journal and source resolve to the same
	// filesystem object. Recovery must never be able to delete its source.
	ErrRecoveryPathAlias = errors.New("inplace: recovery sidecar aliases source")
)

var sidecarMagic = [8]byte{'Q', 'R', 'Y', 'R', 'P', 0, 0, 1}

const (
	committedFlagOffset       = 8 // byte position of the committed flag in the sidecar
	sidecarHeaderBytes  int64 = 8 + 1 + 8 + 8
	sidecarEntryBytes   int64 = 8 + 8
	maxRecoveryEntries        = 64 * 1024
	maxRecoveryRollback int64 = 64 << 20 // 64 MiB, far above the normal 1 MiB edit window
	maxRecoverySidecar        = sidecarHeaderBytes + int64(maxRecoveryEntries)*sidecarEntryBytes + maxRecoveryRollback
	maxInt64                  = int64(^uint64(0) >> 1)
)

// Apply writes patches to path in place, recording a reverse-patch sidecar at
// sidecarPath for crash recovery. Patches must be length-preserving,
// non-overlapping, and within the file; their Old bytes must match the file.
// An existing sidecar is never replaced and causes ErrRecoveryPending.
func Apply(path string, patches []Patch, sidecarPath string) error {
	if len(patches) == 0 {
		return nil
	}
	if pathsLexicallyEqual(path, sidecarPath) {
		return ErrRecoveryPathAlias
	}
	ordered, err := validate(patches)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()

	// Verify current content matches Old (detect concurrent modification).
	buf := make([]byte, 0)
	for _, p := range ordered {
		length := int64(len(p.Old))
		if length > size || p.Offset > size-length {
			return ErrOutOfRange
		}
		if cap(buf) < len(p.Old) {
			buf = make([]byte, len(p.Old))
		}
		cur := buf[:len(p.Old)]
		if _, err := f.ReadAt(cur, p.Offset); err != nil {
			return err
		}
		if !bytes.Equal(cur, p.Old) {
			return ErrDrift
		}
	}

	// 1. Reverse-patch sidecar (committed=0), fsynced before touching the source.
	if err := writeSidecar(sidecarPath, size, ordered); err != nil {
		return err
	}

	// 2. Apply the patches, then fsync the source.
	for _, p := range ordered {
		if _, err := f.WriteAt(p.New, p.Offset); err != nil {
			return fmt.Errorf("inplace: write at %d: %w", p.Offset, err)
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}

	// 3. Mark committed and remove the sidecar.
	if err := markCommitted(sidecarPath); err != nil {
		return err
	}
	return removeSidecar(sidecarPath)
}

// Recover replays or clears a leftover sidecar. It is safe to call when no
// sidecar exists (returns nil). Returns true if it rolled the file back.
//
// This is an internal recovery primitive, not an authorization boundary. The
// caller must authenticate the sidecar and confirm its association with path;
// adjacency alone is not sufficient evidence. Ordinary file open must not call
// Recover automatically.
func Recover(path string, sidecarPath string) (rolledBack bool, err error) {
	if pathsLexicallyEqual(path, sidecarPath) {
		return false, ErrRecoveryPathAlias
	}
	sc, err := os.Open(sidecarPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	info, statErr := sc.Stat()
	if statErr != nil {
		sc.Close()
		return false, statErr
	}
	if !info.Mode().IsRegular() {
		sc.Close()
		return false, fmt.Errorf("%w: sidecar is not a regular file", ErrInvalidSidecar)
	}
	if info.Size() > maxRecoverySidecar {
		sc.Close()
		return false, ErrRecoveryTooLarge
	}
	sourceInfo, sourceStatErr := os.Stat(path)
	if sourceStatErr != nil {
		sc.Close()
		return false, sourceStatErr
	}
	if os.SameFile(sourceInfo, info) {
		sc.Close()
		return false, ErrRecoveryPathAlias
	}
	committed, recordedSize, entries, perr := readSidecar(sc)
	closeErr := sc.Close()
	if perr != nil {
		return false, perr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if committed {
		if err := verifySource(path, recordedSize, info); err != nil {
			return false, err
		}
		return false, removeSidecar(sidecarPath)
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	fileInfo, err := f.Stat()
	if err != nil {
		f.Close()
		return false, err
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Size() != recordedSize {
		f.Close()
		return false, ErrRecoverySourceMismatch
	}
	if os.SameFile(fileInfo, info) {
		f.Close()
		return false, ErrRecoveryPathAlias
	}
	for _, e := range entries {
		if _, err := f.WriteAt(e.bytes, e.offset); err != nil {
			f.Close()
			return false, err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	return true, removeSidecar(sidecarPath)
}

func validate(patches []Patch) ([]Patch, error) {
	if err := validateRecoveryBudget(patches); err != nil {
		return nil, err
	}
	ordered := make([]Patch, len(patches))
	copy(ordered, patches)
	for _, p := range ordered {
		if len(p.New) != len(p.Old) {
			return nil, ErrNotLengthPreserving
		}
		if length := int64(len(p.Old)); p.Offset < 0 || p.Offset > maxInt64-length {
			return nil, ErrOutOfRange
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Offset < ordered[j].Offset })
	for i := 1; i < len(ordered); i++ {
		prevLength := int64(len(ordered[i-1].Old))
		prevEnd := ordered[i-1].Offset + prevLength
		if ordered[i].Offset < prevEnd {
			return nil, ErrOverlap
		}
	}
	return ordered, nil
}

type entry struct {
	offset int64
	bytes  []byte
}

func writeSidecar(path string, fileSize int64, patches []Patch) error {
	if fileSize < 0 {
		return ErrInvalidSidecar
	}
	if err := validateRecoveryBudget(patches); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrRecoveryPending
	}
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if _, err := w.Write(sidecarMagic[:]); err != nil {
		f.Close()
		return err
	}
	if err := w.WriteByte(0); err != nil { // committed flag = 0
		f.Close()
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, fileSize); err != nil {
		f.Close()
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, int64(len(patches))); err != nil {
		f.Close()
		return err
	}
	for _, p := range patches {
		if err := binary.Write(w, binary.LittleEndian, p.Offset); err != nil {
			f.Close()
			return err
		}
		if err := binary.Write(w, binary.LittleEndian, int64(len(p.Old))); err != nil {
			f.Close()
			return err
		}
		if _, err := w.Write(p.Old); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncParentDirectory(path)
}

func markCommitted(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt([]byte{1}, committedFlagOffset); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readSidecar(r io.Reader) (committed bool, fileSize int64, entries []entry, err error) {
	var magic [8]byte
	if _, err = io.ReadFull(r, magic[:]); err != nil {
		return false, 0, nil, fmt.Errorf("%w: %v", ErrInvalidSidecar, err)
	}
	if magic != sidecarMagic {
		return false, 0, nil, fmt.Errorf("%w: bad magic", ErrInvalidSidecar)
	}
	var flag [1]byte
	if _, err = io.ReadFull(r, flag[:]); err != nil {
		return false, 0, nil, fmt.Errorf("%w: %v", ErrInvalidSidecar, err)
	}
	if flag[0] != 0 && flag[0] != 1 {
		return false, 0, nil, fmt.Errorf("%w: bad committed flag", ErrInvalidSidecar)
	}
	committed = flag[0] == 1
	var count int64
	if err = binary.Read(r, binary.LittleEndian, &fileSize); err != nil {
		return false, 0, nil, fmt.Errorf("%w: %v", ErrInvalidSidecar, err)
	}
	if fileSize < 0 {
		return false, 0, nil, fmt.Errorf("%w: bad source size", ErrInvalidSidecar)
	}
	if err = binary.Read(r, binary.LittleEndian, &count); err != nil {
		return false, 0, nil, fmt.Errorf("%w: %v", ErrInvalidSidecar, err)
	}
	if count < 0 {
		return false, 0, nil, fmt.Errorf("%w: bad entry count", ErrInvalidSidecar)
	}
	if count > maxRecoveryEntries {
		return false, 0, nil, ErrRecoveryTooLarge
	}
	entries = make([]entry, 0, int(count))
	var totalBytes, previousEnd int64
	for i := int64(0); i < count; i++ {
		var off, n int64
		if err = binary.Read(r, binary.LittleEndian, &off); err != nil {
			return false, 0, nil, fmt.Errorf("%w: %v", ErrInvalidSidecar, err)
		}
		if err = binary.Read(r, binary.LittleEndian, &n); err != nil {
			return false, 0, nil, fmt.Errorf("%w: %v", ErrInvalidSidecar, err)
		}
		if off < 0 || n < 0 || off > fileSize || n > fileSize-off {
			return false, 0, nil, fmt.Errorf("%w: entry range is outside source", ErrInvalidSidecar)
		}
		if i > 0 && off < previousEnd {
			return false, 0, nil, fmt.Errorf("%w: entries overlap or are unsorted", ErrInvalidSidecar)
		}
		if n > maxRecoveryRollback-totalBytes {
			return false, 0, nil, ErrRecoveryTooLarge
		}
		b := make([]byte, n)
		if _, err = io.ReadFull(r, b); err != nil {
			return false, 0, nil, fmt.Errorf("%w: %v", ErrInvalidSidecar, err)
		}
		entries = append(entries, entry{offset: off, bytes: b})
		totalBytes += n
		previousEnd = off + n
	}
	var trailing [1]byte
	if n, readErr := io.ReadFull(r, trailing[:]); n != 0 || !errors.Is(readErr, io.EOF) {
		return false, 0, nil, fmt.Errorf("%w: trailing data", ErrInvalidSidecar)
	}
	return committed, fileSize, entries, nil
}

func validateRecoveryBudget(patches []Patch) error {
	if len(patches) > maxRecoveryEntries {
		return ErrRecoveryTooLarge
	}
	var total int64
	for _, patch := range patches {
		n := int64(len(patch.Old))
		if n > maxRecoveryRollback-total {
			return ErrRecoveryTooLarge
		}
		total += n
	}
	return nil
}

func verifySource(path string, recordedSize int64, sidecarInfo os.FileInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	closeErr := f.Close()
	if statErr != nil {
		return statErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !info.Mode().IsRegular() || info.Size() != recordedSize {
		return ErrRecoverySourceMismatch
	}
	if os.SameFile(info, sidecarInfo) {
		return ErrRecoveryPathAlias
	}
	return nil
}

func pathsLexicallyEqual(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	absA = filepath.Clean(absA)
	absB = filepath.Clean(absB)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(absA, absB)
	}
	return absA == absB
}

func removeSidecar(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncParentDirectory(path)
}

// Windows does not expose a portable directory fsync through os.File.Sync.
// On platforms that do, syncing the parent makes sidecar creation/removal
// durable across power loss rather than only durable in the file cache.
func syncParentDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		dir.Close()
		return err
	}
	return dir.Close()
}
