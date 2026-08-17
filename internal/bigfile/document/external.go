package document

import (
	"errors"
	"os"
	"time"

	"novera/internal/bigfile/regularfile"
)

// FileState captures the on-disk state of the currently opened file.
type FileState struct {
	Size    int64
	ModTime time.Time
}

// ExternalModification compares the original open state to the current file on disk.
type ExternalModification struct {
	Original FileState
	Current  FileState
	Metadata Metadata
}

func (s FileState) Equal(other FileState) bool {
	return s.Size == other.Size && s.ModTime.Equal(other.ModTime)
}

func (d *FileDocument) OriginalFileState() FileState {
	return FileState{Size: d.size, ModTime: d.mtime}
}

// OpenedFileInfo returns the identity of the exact descriptor backing this
// document. Output producers use it to reject hard-link and renamed aliases of
// the opened source, rather than trusting a pathname that may have changed.
func (d *FileDocument) OpenedFileInfo() (os.FileInfo, error) {
	if err := d.beginRead(); err != nil {
		return nil, err
	}
	defer d.endRead()
	return d.file.Stat()
}

// CurrentPathIdentity returns the current pathname state and reports whether
// that path still names the exact retained file handle. Unlike a size/mtime
// comparison, this detects rename-and-recreate log rotation even when the new
// file happens to have identical metadata.
func (d *FileDocument) CurrentPathIdentity() (FileState, bool, error) {
	if err := d.beginRead(); err != nil {
		return FileState{}, false, err
	}
	defer d.endRead()
	openedInfo, err := d.file.Stat()
	if err != nil {
		return FileState{}, false, err
	}
	pathInfo, err := os.Stat(d.path)
	if err != nil {
		return FileState{}, false, err
	}
	if !pathInfo.Mode().IsRegular() {
		return FileState{}, false, &os.PathError{Op: "stat", Path: d.path, Err: regularfile.ErrNotRegular}
	}
	return FileState{Size: pathInfo.Size(), ModTime: pathInfo.ModTime()}, os.SameFile(openedInfo, pathInfo), nil
}

func (d *FileDocument) CurrentFileState() (FileState, error) {
	st, err := os.Stat(d.path)
	if err != nil {
		return FileState{}, err
	}
	if !st.Mode().IsRegular() {
		return FileState{}, &os.PathError{Op: "stat", Path: d.path, Err: regularfile.ErrNotRegular}
	}
	return FileState{Size: st.Size(), ModTime: st.ModTime()}, nil
}

func (d *FileDocument) InspectExternalModification() (ExternalModification, bool, error) {
	current, sameIdentity, err := d.CurrentPathIdentity()
	if err != nil {
		return ExternalModification{}, false, err
	}
	original := d.OriginalFileState()
	if sameIdentity && original.Equal(current) {
		return ExternalModification{}, false, nil
	}

	reopened, err := regularfile.Open(d.path)
	if err != nil {
		return ExternalModification{}, true, err
	}
	defer reopened.Close()
	openedInfo, err := reopened.Stat()
	if err != nil {
		return ExternalModification{}, true, err
	}
	openedState := FileState{Size: openedInfo.Size(), ModTime: openedInfo.ModTime()}
	if !openedState.Equal(current) {
		return ExternalModification{}, true, errors.New("source changed while external modification metadata was opened")
	}
	sample, err := readSample(reopened, openedState.Size, openSampleSize)
	if err != nil {
		return ExternalModification{}, true, err
	}
	if err := validateExternalModificationRead(d.path, reopened, openedInfo); err != nil {
		return ExternalModification{}, true, err
	}

	return ExternalModification{
		Original: original,
		Current:  openedState,
		Metadata: detectMetadata(d.path, openedState.Size, sample),
	}, true, nil
}

func validateExternalModificationRead(path string, reopened *os.File, openedInfo os.FileInfo) error {
	if reopened == nil || openedInfo == nil {
		return errors.New("opened external-modification source is required")
	}
	finalInfo, err := reopened.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, finalInfo) ||
		!os.SameFile(finalInfo, pathInfo) || finalInfo.Size() != openedInfo.Size() ||
		!finalInfo.ModTime().Equal(openedInfo.ModTime()) || pathInfo.Size() != finalInfo.Size() ||
		!pathInfo.ModTime().Equal(finalInfo.ModTime()) {
		return errors.New("source changed while external modification metadata was read")
	}
	return nil
}
