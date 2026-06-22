// Package inplace applies length-preserving byte patches directly to a file,
// crash-safely. It is the fast save path for huge files: when staged edits do
// not change the file's total byte length, only the changed extents are
// overwritten (O(edits)) instead of rewriting the whole file (O(size)).
//
// Crash safety uses a reverse-patch sidecar (a write-ahead log of the original
// bytes) written and fsynced BEFORE the source is touched:
//
//	1. write sidecar (committed=0) with the original bytes of every extent; fsync
//	2. overwrite each extent in the source with the new bytes; fsync source
//	3. mark the sidecar committed=1; fsync; remove it
//
// On the next open, Recover inspects a leftover sidecar:
//   - committed -> the patch finished; just delete the sidecar
//   - not committed -> the patch may be partial; replay the original bytes to
//     roll the source back to its pre-patch state, then delete the sidecar
//   - unparseable/partial -> patching never began (the sidecar is fsynced first),
//     so the source is untouched; delete the sidecar
package inplace

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
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
)

var sidecarMagic = [8]byte{'Q', 'R', 'Y', 'R', 'P', 0, 0, 1}

const committedFlagOffset = 8 // byte position of the committed flag in the sidecar

// Apply writes patches to path in place, recording a reverse-patch sidecar at
// sidecarPath for crash recovery. Patches must be length-preserving,
// non-overlapping, and within the file; their Old bytes must match the file.
func Apply(path string, patches []Patch, sidecarPath string) error {
	if len(patches) == 0 {
		return nil
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
		if p.Offset+int64(len(p.Old)) > size {
			return ErrOutOfRange
		}
		if cap(buf) < len(p.Old) {
			buf = make([]byte, len(p.Old))
		}
		cur := buf[:len(p.Old)]
		if _, err := f.ReadAt(cur, p.Offset); err != nil {
			return err
		}
		if string(cur) != string(p.Old) {
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
	return os.Remove(sidecarPath)
}

// Recover replays or clears a leftover sidecar. It is safe to call when no
// sidecar exists (returns nil). Returns true if it rolled the file back.
func Recover(path string, sidecarPath string) (rolledBack bool, err error) {
	sc, err := os.Open(sidecarPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	committed, entries, perr := readSidecar(sc)
	sc.Close()
	if perr != nil {
		// Partial/corrupt sidecar => patching never began; nothing to undo.
		return false, os.Remove(sidecarPath)
	}
	if committed {
		return false, os.Remove(sidecarPath)
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
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
	f.Close()
	return true, os.Remove(sidecarPath)
}

func validate(patches []Patch) ([]Patch, error) {
	ordered := make([]Patch, len(patches))
	copy(ordered, patches)
	for _, p := range ordered {
		if len(p.New) != len(p.Old) {
			return nil, ErrNotLengthPreserving
		}
		if p.Offset < 0 {
			return nil, ErrOutOfRange
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Offset < ordered[j].Offset })
	for i := 1; i < len(ordered); i++ {
		prevEnd := ordered[i-1].Offset + int64(len(ordered[i-1].Old))
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
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
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
	return f.Close()
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

func readSidecar(r io.Reader) (committed bool, entries []entry, err error) {
	var magic [8]byte
	if _, err = io.ReadFull(r, magic[:]); err != nil {
		return false, nil, err
	}
	if magic != sidecarMagic {
		return false, nil, errors.New("inplace: bad sidecar magic")
	}
	var flag [1]byte
	if _, err = io.ReadFull(r, flag[:]); err != nil {
		return false, nil, err
	}
	committed = flag[0] == 1
	var fileSize, count int64
	if err = binary.Read(r, binary.LittleEndian, &fileSize); err != nil {
		return false, nil, err
	}
	if err = binary.Read(r, binary.LittleEndian, &count); err != nil {
		return false, nil, err
	}
	if count < 0 {
		return false, nil, errors.New("inplace: bad sidecar entry count")
	}
	for i := int64(0); i < count; i++ {
		var off, n int64
		if err = binary.Read(r, binary.LittleEndian, &off); err != nil {
			return false, nil, err
		}
		if err = binary.Read(r, binary.LittleEndian, &n); err != nil {
			return false, nil, err
		}
		if n < 0 {
			return false, nil, errors.New("inplace: bad sidecar entry length")
		}
		b := make([]byte, n)
		if _, err = io.ReadFull(r, b); err != nil {
			return false, nil, err
		}
		entries = append(entries, entry{offset: off, bytes: b})
	}
	return committed, entries, nil
}
