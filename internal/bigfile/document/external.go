package document

import (
	"os"
	"time"
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

func (d *FileDocument) CurrentFileState() (FileState, error) {
	st, err := os.Stat(d.path)
	if err != nil {
		return FileState{}, err
	}
	return FileState{Size: st.Size(), ModTime: st.ModTime()}, nil
}

func (d *FileDocument) InspectExternalModification() (ExternalModification, bool, error) {
	current, err := d.CurrentFileState()
	if err != nil {
		return ExternalModification{}, false, err
	}
	original := d.OriginalFileState()
	if original.Equal(current) {
		return ExternalModification{}, false, nil
	}

	reopened, err := os.Open(d.path)
	if err != nil {
		return ExternalModification{}, true, err
	}
	defer reopened.Close()
	sample, err := readSample(reopened, current.Size, openSampleSize)
	if err != nil {
		return ExternalModification{}, true, err
	}

	return ExternalModification{
		Original: original,
		Current:  current,
		Metadata: detectMetadata(d.path, current.Size, sample),
	}, true, nil
}
