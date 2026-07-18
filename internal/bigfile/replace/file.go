package replace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"novera/internal/bigfile/fileio"
)

var (
	openSourceFile = os.Open
	openExclusive  = func(path string) (syncWriteCloser, error) {
		return fileio.OpenExclusiveOutput(path, 0o600)
	}
	renamePath      = fileio.Rename
	removePath      = fileio.Remove
	statPath        = fileio.Stat
	openManifestOut = func(path string, exclusive bool) (io.WriteCloser, error) {
		flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		if exclusive {
			flag |= os.O_EXCL
		}
		return os.OpenFile(path, flag, 0o600)
	}
)

var ErrSourceModifiedDuringOperation = errors.New("source file modified during operation")

type sourceSnapshot struct {
	size    int64
	modTime time.Time
}

// FileOptions controls replace-all file transforms.
type FileOptions struct {
	ChunkSize             int
	CaseInsensitive       bool
	WholeWord             bool
	DeletePartialOnCancel bool
	SwapOriginal          bool
	BackupPath            string
	Progress              func(Progress)
}

// Manifest records a file transform operation for recovery and auditing.
type Manifest struct {
	Operation      string     `json:"operation"`
	Source         string     `json:"source"`
	Output         string     `json:"output"`
	TempOutput     string     `json:"tempOutput"`
	Phase          string     `json:"phase,omitempty"`
	StartedAt      time.Time  `json:"startedAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	SourceSize     int64      `json:"sourceSize"`
	SourceModTime  int64      `json:"sourceModTime,omitempty"`
	BytesProcessed int64      `json:"bytesProcessed"`
	Matches        int64      `json:"matches"`
	ConflictCount  int64      `json:"conflictCount,omitempty"`
	Backup         string     `json:"backup,omitempty"`
	BackupPlanned  string     `json:"backupPlanned,omitempty"`
	SwapRequested  bool       `json:"swapRequested,omitempty"`
	Swapped        bool       `json:"swapped,omitempty"`
	Status         string     `json:"status"`
	Error          string     `json:"error,omitempty"`
}

// FileSummary describes the result of a file transform.
type FileSummary struct {
	OutputPath   string
	TempPath     string
	ManifestPath string
	BackupPath   string
	Swapped      bool
	Matches      int64
	Conflicts    int64
}

// ReplacePlainFile streams sourcePath into outputPath through an exclusive temp file.
func ReplacePlainFile(ctx context.Context, sourcePath string, outputPath string, pattern []byte, repl []byte, opts FileOptions) (FileSummary, error) {
	same, err := samePath(sourcePath, outputPath)
	if err != nil {
		return FileSummary{}, err
	}
	if same {
		return FileSummary{}, errors.New("output path must be different from source path")
	}

	summary := FileSummary{
		OutputPath:   outputPath,
		TempPath:     outputPath + ".quarry.tmp",
		ManifestPath: outputPath + ".quarry.manifest.json",
	}

	if _, err := statPath(outputPath); err == nil {
		return summary, errors.New("output file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return summary, err
	}

	backupPath := opts.BackupPath
	if opts.SwapOriginal {
		if backupPath == "" {
			backupPath = sourcePath + ".quarry.bak"
		}
		sameBackup, err := samePath(sourcePath, backupPath)
		if err != nil {
			return summary, err
		}
		if sameBackup {
			return summary, errors.New("backup path must be different from source path")
		}
		if _, err := statPath(backupPath); err == nil {
			return summary, errors.New("backup file already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return summary, err
		}
		summary.BackupPath = backupPath
	}

	src, err := openSourceFile(sourcePath)
	if err != nil {
		return summary, err
	}
	srcClosed := false
	defer func() {
		if !srcClosed {
			_ = src.Close()
		}
	}()

	st, err := src.Stat()
	if err != nil {
		return summary, err
	}
	sourceState := snapshotSource(st)

	dst, err := openExclusive(summary.TempPath)
	if err != nil {
		return summary, err
	}

	manifest := Manifest{
		Operation:     "plain-replace",
		Source:        sourcePath,
		Output:        outputPath,
		TempOutput:    summary.TempPath,
		Phase:         "processing",
		StartedAt:     time.Now().UTC(),
		SourceSize:    st.Size(),
		SourceModTime: st.ModTime().UnixNano(),
		BackupPlanned: backupPath,
		SwapRequested: opts.SwapOriginal,
		Status:        "running",
	}
	if err := writeManifest(summary.ManifestPath, manifest, true); err != nil {
		_ = dst.Close()
		_ = removePath(summary.TempPath)
		return summary, err
	}

	plainOpts := PlainOptions{
		ChunkSize:       opts.ChunkSize,
		CaseInsensitive: opts.CaseInsensitive,
		WholeWord:       opts.WholeWord,
		Progress: func(p Progress) {
			manifest.BytesProcessed = p.BytesProcessed
			manifest.Matches = p.Matches
			if opts.Progress != nil {
				opts.Progress(p)
			}
		},
	}
	matches, replaceErr := ReplacePlain(ctx, src, dst, pattern, repl, plainOpts)
	manifest.Matches = matches
	closeErr := dst.Close()

	if replaceErr != nil {
		failFileTransformWrite(summary, &manifest, opts, replaceErr)
		return summary, replaceErr
	}
	if closeErr != nil {
		writeFailedManifest(summary.ManifestPath, &manifest, closeErr)
		return summary, closeErr
	}
	if err := writeReadyToFinalizeManifestOrFail(summary.ManifestPath, &manifest); err != nil {
		return summary, err
	}

	if err := renamePath(summary.TempPath, outputPath); err != nil {
		writeFailedManifest(summary.ManifestPath, &manifest, err)
		return summary, err
	}
	if err := writeManifestPhaseOrFail(summary.ManifestPath, &manifest, "output_written"); err != nil {
		return summary, err
	}

	if opts.SwapOriginal {
		if err := verifySourceUnchanged(sourcePath, sourceState); err != nil {
			writeFailedManifest(summary.ManifestPath, &manifest, err)
			return summary, err
		}
		if err := src.Close(); err != nil {
			srcClosed = true
			writeFailedManifest(summary.ManifestPath, &manifest, err)
			return summary, err
		}
		srcClosed = true
		if err := swapOutputIntoSource(sourcePath, outputPath, backupPath); err != nil {
			writeFailedManifest(summary.ManifestPath, &manifest, err)
			return summary, err
		}
		manifest.Backup = backupPath
		manifest.Swapped = true
		summary.BackupPath = backupPath
		summary.Swapped = true
		if err := writeManifestPhaseOrFail(summary.ManifestPath, &manifest, "swapped"); err != nil {
			return summary, err
		}
	}

	if err := writeCompletedManifestOrFail(summary.ManifestPath, &manifest, st.Size()); err != nil {
		return summary, err
	}

	summary.Matches = matches
	return summary, nil
}

func writeFailedManifest(path string, manifest *Manifest, err error) {
	if manifest == nil || err == nil {
		return
	}
	manifest.Status = "failed"
	if errors.Is(err, context.Canceled) {
		manifest.Status = "canceled"
	}
	manifest.Error = err.Error()
	_ = writeManifest(path, *manifest, false)
}

func failFileTransformWrite(summary FileSummary, manifest *Manifest, opts FileOptions, err error) {
	writeFailedManifest(summary.ManifestPath, manifest, err)
	if manifest != nil && manifest.Status == "canceled" && opts.DeletePartialOnCancel {
		_ = removePath(summary.TempPath)
	}
}

func writeReadyToFinalizeManifest(path string, manifest *Manifest) error {
	if manifest == nil {
		return errors.New("manifest is required")
	}
	manifest.Phase = "ready_to_finalize"
	return writeManifest(path, *manifest, false)
}

func writeReadyToFinalizeManifestOrFail(path string, manifest *Manifest) error {
	if err := writeReadyToFinalizeManifest(path, manifest); err != nil {
		writeFailedManifest(path, manifest, err)
		return err
	}
	return nil
}

func writeManifestPhaseOrFail(path string, manifest *Manifest, phase string) error {
	if manifest == nil {
		return errors.New("manifest is required")
	}
	manifest.Phase = phase
	if err := writeManifest(path, *manifest, false); err != nil {
		writeFailedManifest(path, manifest, err)
		return err
	}
	return nil
}

func writeCompletedManifest(path string, manifest *Manifest, bytesProcessed int64) error {
	if manifest == nil {
		return errors.New("manifest is required")
	}
	now := time.Now().UTC()
	manifest.Status = "complete"
	manifest.Phase = "complete"
	manifest.CompletedAt = &now
	manifest.BytesProcessed = bytesProcessed
	return writeManifest(path, *manifest, false)
}

func writeCompletedManifestOrFail(path string, manifest *Manifest, bytesProcessed int64) error {
	if err := writeCompletedManifest(path, manifest, bytesProcessed); err != nil {
		writeFailedManifest(path, manifest, err)
		return err
	}
	return nil
}

func writeManifest(path string, manifest Manifest, exclusive bool) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	seed, err := os.CreateTemp(dir, ".novera-recovery-*.tmp")
	if err != nil {
		return err
	}
	tempPath := seed.Name()
	if err := seed.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()

	f, err := openManifestOut(tempPath, true)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		if syncer, ok := f.(interface{ Sync() error }); ok {
			writeErr = syncer.Sync()
		} else {
			writeErr = errors.New("manifest writer does not support sync")
		}
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}

	if exclusive {
		if err := os.Link(tempPath, path); err != nil {
			return err
		}
		if err := os.Remove(tempPath); err != nil {
			return err
		}
	} else {
		if err := os.Rename(tempPath, path); err != nil {
			return err
		}
	}
	cleanup = false
	return syncManifestDirectory(path)
}

func syncManifestDirectory(path string) error {
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

func snapshotSource(info os.FileInfo) sourceSnapshot {
	return sourceSnapshot{
		size:    info.Size(),
		modTime: info.ModTime(),
	}
}

func verifySourceUnchanged(path string, before sourceSnapshot) error {
	info, err := statPath(path)
	if err != nil {
		return err
	}
	after := snapshotSource(info)
	if after.size != before.size || !after.modTime.Equal(before.modTime) {
		return ErrSourceModifiedDuringOperation
	}
	return nil
}

func samePath(a string, b string) (bool, error) {
	return fileio.SamePath(a, b)
}

func swapOutputIntoSource(sourcePath string, outputPath string, backupPath string) error {
	sourceInfo, err := statPath(sourcePath)
	if err != nil {
		return err
	}
	if err := renamePath(sourcePath, backupPath); err != nil {
		return err
	}
	if err := fileio.ApplyMode(outputPath, sourceInfo.Mode()); err != nil {
		rollbackErr := renamePath(backupPath, sourcePath)
		if rollbackErr != nil {
			return fmt.Errorf("prepare swapped output metadata failed: %w (rollback failed: %v)", err, rollbackErr)
		}
		return err
	}
	if err := renamePath(outputPath, sourcePath); err != nil {
		rollbackErr := renamePath(backupPath, sourcePath)
		if rollbackErr != nil {
			return fmt.Errorf("swap finalize failed: %w (rollback failed: %v)", err, rollbackErr)
		}
		return err
	}
	return nil
}

func applyBackupModeToOutput(outputPath string, backupPath string) error {
	backupInfo, err := statPath(backupPath)
	if err != nil {
		return err
	}
	return fileio.ApplyMode(outputPath, backupInfo.Mode())
}
