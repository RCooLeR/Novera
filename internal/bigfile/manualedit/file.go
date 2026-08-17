package manualedit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/regularfile"
)

var (
	openSourceFile = regularfile.Open
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

var (
	ErrSourceModifiedDuringOperation = errors.New("source file modified during operation")
	ErrSwapOriginalDisabled          = errors.New("manual-edit source replacement is disabled; save a copy instead")
)

const (
	manifestProgressFlushBytes    int64 = 16 * 1024 * 1024
	manifestProgressFlushInterval       = 2 * time.Second
)

type FileOptions struct {
	DeletePartialOnCancel bool
	SwapOriginal          bool
	BackupPath            string
	MaxInsertedBytes      int64
	Progress              func(Progress)
}

type FileSummary struct {
	OutputPath    string
	TempPath      string
	ManifestPath  string
	BackupPath    string
	Swapped       bool
	BytesWritten  int64
	ModifiedRange Range
}

type Manifest struct {
	Operation      string     `json:"operation"`
	Source         string     `json:"source"`
	Output         string     `json:"output"`
	TempOutput     string     `json:"tempOutput"`
	StartedAt      time.Time  `json:"startedAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	SourceSize     int64      `json:"sourceSize"`
	BytesProcessed int64      `json:"bytesProcessed"`
	Backup         string     `json:"backup,omitempty"`
	Swapped        bool       `json:"swapped,omitempty"`
	Status         string     `json:"status"`
	Error          string     `json:"error,omitempty"`
	EditStart      int64      `json:"editStart"`
	EditEnd        int64      `json:"editEnd"`
	InsertedBytes  int64      `json:"insertedBytes"`
}

type sourceSnapshot struct {
	size    int64
	modTime time.Time
}

func ApplyFileEdit(ctx context.Context, sourcePath string, outputPath string, edit Edit, opts FileOptions) (FileSummary, error) {
	if opts.SwapOriginal {
		return FileSummary{
			OutputPath: outputPath,
			ModifiedRange: Range{
				Start: edit.Start,
				End:   max64(edit.End, edit.Start+int64(len(edit.Text))),
			},
		}, ErrSwapOriginalDisabled
	}
	summary, backupPath, err := prepareManualEditOutput(sourcePath, outputPath, opts, Range{
		Start: edit.Start,
		End:   max64(edit.End, edit.Start+int64(len(edit.Text))),
	})
	if err != nil {
		return summary, err
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

	table := NewPieceTable(st.Size())
	if opts.MaxInsertedBytes > 0 {
		table.SetMaxInsertedBytes(opts.MaxInsertedBytes)
	}
	if err := table.Replace(edit.Start, edit.End, edit.Text); err != nil {
		return summary, err
	}

	dst, err := openExclusive(summary.TempPath)
	if err != nil {
		return summary, err
	}

	manifest := Manifest{
		Operation:     "manual-range-edit",
		Source:        sourcePath,
		Output:        outputPath,
		TempOutput:    summary.TempPath,
		StartedAt:     time.Now().UTC(),
		SourceSize:    st.Size(),
		Status:        "running",
		EditStart:     edit.Start,
		EditEnd:       edit.End,
		InsertedBytes: int64(len(edit.Text)),
	}
	if err := writeManifest(summary.ManifestPath, manifest, true); err != nil {
		_ = dst.Close()
		_ = removePath(summary.TempPath)
		return summary, err
	}

	progress := manifestProgressFlusher(summary.ManifestPath, &manifest, opts.Progress)
	bytesWritten, writeErr := table.WriteTo(ctx, fileReaderAtSize{file: src, size: st.Size()}, dst, WriteOptions{Progress: progress})
	closeErr := dst.Close()
	summary.BytesWritten = bytesWritten

	if writeErr != nil {
		failManualEditWrite(summary, &manifest, opts, writeErr)
		return summary, writeErr
	}
	if closeErr != nil {
		writeFailedManifest(summary.ManifestPath, &manifest, closeErr)
		return summary, closeErr
	}

	summary, closed, err := finalizeManualEditOutput(sourcePath, outputPath, backupPath, src, sourceState, summary, &manifest, opts)
	if closed {
		srcClosed = true
	}
	if err != nil {
		return summary, err
	}

	return summary, nil
}

func WriteSessionToFile(ctx context.Context, sourcePath string, outputPath string, session *Session, opts FileOptions) (FileSummary, error) {
	if opts.SwapOriginal {
		return FileSummary{OutputPath: outputPath}, ErrSwapOriginalDisabled
	}
	if session == nil || !session.HasEdits() {
		return FileSummary{}, errors.New("session has no staged edits")
	}
	return writeTableToFile(ctx, sourcePath, outputPath, session.table, session.SourceMappedModifiedRanges(), opts, "manual-edit-session")
}

func prepareManualEditOutput(sourcePath string, outputPath string, opts FileOptions, modified Range) (FileSummary, string, error) {
	if opts.SwapOriginal {
		return FileSummary{OutputPath: outputPath, ModifiedRange: modified}, "", ErrSwapOriginalDisabled
	}
	same, err := samePath(sourcePath, outputPath)
	if err != nil {
		return FileSummary{}, "", err
	}
	if same {
		return FileSummary{}, "", errors.New("output path must be different from source path")
	}

	summary := FileSummary{
		OutputPath:    outputPath,
		TempPath:      outputPath + ".quarry.tmp",
		ManifestPath:  outputPath + ".quarry.manifest.json",
		ModifiedRange: modified,
	}

	if _, err := statPath(outputPath); err == nil {
		return summary, "", errors.New("output file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return summary, "", err
	}

	backupPath := opts.BackupPath
	if opts.SwapOriginal {
		if backupPath == "" {
			backupPath = sourcePath + ".quarry.bak"
		}
		sameBackup, err := samePath(sourcePath, backupPath)
		if err != nil {
			return summary, "", err
		}
		if sameBackup {
			return summary, "", errors.New("backup path must be different from source path")
		}
		if _, err := statPath(backupPath); err == nil {
			return summary, "", errors.New("backup file already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return summary, "", err
		}
		summary.BackupPath = backupPath
	}

	return summary, backupPath, nil
}

func writeManifest(path string, manifest Manifest, exclusive bool) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	f, err := openManifestOut(path, exclusive)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func manifestProgressFlusher(path string, manifest *Manifest, progress func(Progress)) func(Progress) {
	var lastBytes int64
	lastFlush := time.Now()
	return func(p Progress) {
		if manifest != nil {
			manifest.BytesProcessed = p.BytesWritten
			now := time.Now()
			if shouldFlushManifestProgress(p.BytesWritten, lastBytes, lastFlush, now) {
				if writeManifest(path, *manifest, false) == nil {
					lastBytes = p.BytesWritten
					lastFlush = now
				}
			}
		}
		if progress != nil {
			progress(p)
		}
	}
}

func shouldFlushManifestProgress(bytesWritten int64, lastBytes int64, lastFlush time.Time, now time.Time) bool {
	if bytesWritten <= 0 {
		return false
	}
	if bytesWritten-lastBytes >= manifestProgressFlushBytes {
		return true
	}
	return now.Sub(lastFlush) >= manifestProgressFlushInterval
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

func writeCompletedManifest(path string, manifest *Manifest, bytesWritten int64) error {
	if manifest == nil {
		return errors.New("manifest is required")
	}
	now := time.Now().UTC()
	manifest.Status = "complete"
	manifest.CompletedAt = &now
	manifest.BytesProcessed = bytesWritten
	return writeManifest(path, *manifest, false)
}

func writeCompletedManifestOrFail(path string, manifest *Manifest, bytesWritten int64) error {
	if err := writeCompletedManifest(path, manifest, bytesWritten); err != nil {
		writeFailedManifest(path, manifest, err)
		return err
	}
	return nil
}

func failManualEditWrite(summary FileSummary, manifest *Manifest, opts FileOptions, err error) {
	writeFailedManifest(summary.ManifestPath, manifest, err)
	if manifest != nil && manifest.Status == "canceled" && opts.DeletePartialOnCancel {
		_ = removePath(summary.TempPath)
	}
}

func finalizeManualEditOutput(sourcePath string, outputPath string, backupPath string, src io.Closer, sourceState sourceSnapshot, summary FileSummary, manifest *Manifest, opts FileOptions) (FileSummary, bool, error) {
	if err := renamePath(summary.TempPath, outputPath); err != nil {
		writeFailedManifest(summary.ManifestPath, manifest, err)
		return summary, false, err
	}

	sourceClosed := false
	if opts.SwapOriginal {
		if err := verifySourceUnchanged(sourcePath, sourceState); err != nil {
			writeFailedManifest(summary.ManifestPath, manifest, err)
			return summary, sourceClosed, err
		}
		if err := src.Close(); err != nil {
			sourceClosed = true
			writeFailedManifest(summary.ManifestPath, manifest, err)
			return summary, sourceClosed, err
		}
		sourceClosed = true
		if err := swapOutputIntoSource(sourcePath, outputPath, backupPath); err != nil {
			writeFailedManifest(summary.ManifestPath, manifest, err)
			return summary, sourceClosed, err
		}
		manifest.Backup = backupPath
		manifest.Swapped = true
		summary.BackupPath = backupPath
		summary.Swapped = true
	}

	if err := writeCompletedManifestOrFail(summary.ManifestPath, manifest, summary.BytesWritten); err != nil {
		return summary, sourceClosed, err
	}
	return summary, sourceClosed, nil
}

func writeTableToFile(ctx context.Context, sourcePath string, outputPath string, table *PieceTable, modified []Range, opts FileOptions, operation string) (FileSummary, error) {
	if opts.SwapOriginal {
		return FileSummary{OutputPath: outputPath}, ErrSwapOriginalDisabled
	}
	modifiedRange := Range{}
	if len(modified) > 0 {
		modifiedRange = modified[0]
	}

	summary, backupPath, err := prepareManualEditOutput(sourcePath, outputPath, opts, modifiedRange)
	if err != nil {
		return summary, err
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
	// The piece table's offsets are relative to the source size captured when
	// staging began. If the source changed size since then (e.g. a still-growing
	// or re-exported dump), those offsets are stale — abort before writing rather
	// than streaming a truncated/misaligned copy and reporting it as a success.
	if st.Size() != table.OriginalSize() {
		return summary, fmt.Errorf("source size changed from %d to %d since staging: %w",
			table.OriginalSize(), st.Size(), ErrSourceModifiedDuringOperation)
	}
	sourceState := snapshotSource(st)

	dst, err := openExclusive(summary.TempPath)
	if err != nil {
		return summary, err
	}

	editStart := int64(0)
	editEnd := int64(0)
	if len(modified) > 0 {
		editStart = modified[0].Start
		editEnd = modified[0].End
		for _, r := range modified[1:] {
			if r.Start < editStart {
				editStart = r.Start
			}
			if r.End > editEnd {
				editEnd = r.End
			}
		}
	}
	manifest := Manifest{
		Operation:     operation,
		Source:        sourcePath,
		Output:        outputPath,
		TempOutput:    summary.TempPath,
		StartedAt:     time.Now().UTC(),
		SourceSize:    st.Size(),
		Status:        "running",
		EditStart:     editStart,
		EditEnd:       editEnd,
		InsertedBytes: int64(len(table.added)),
	}
	if err := writeManifest(summary.ManifestPath, manifest, true); err != nil {
		_ = dst.Close()
		_ = removePath(summary.TempPath)
		return summary, err
	}

	progress := manifestProgressFlusher(summary.ManifestPath, &manifest, opts.Progress)
	bytesWritten, writeErr := table.WriteTo(ctx, fileReaderAtSize{file: src, size: st.Size()}, dst, WriteOptions{Progress: progress})
	closeErr := dst.Close()
	summary.BytesWritten = bytesWritten

	if writeErr != nil {
		failManualEditWrite(summary, &manifest, opts, writeErr)
		return summary, writeErr
	}
	if closeErr != nil {
		writeFailedManifest(summary.ManifestPath, &manifest, closeErr)
		return summary, closeErr
	}

	summary, closed, err := finalizeManualEditOutput(sourcePath, outputPath, backupPath, src, sourceState, summary, &manifest, opts)
	if closed {
		srcClosed = true
	}
	if err != nil {
		return summary, err
	}
	return summary, nil
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

type fileReaderAtSize struct {
	file *os.File
	size int64
}

func (r fileReaderAtSize) ReadAt(p []byte, off int64) (int, error) {
	return r.file.ReadAt(p, off)
}

func (r fileReaderAtSize) Size() int64 {
	return r.size
}

type syncWriteCloser interface {
	syncWriter
	io.Closer
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
