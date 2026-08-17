package replace

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"novera/internal/bigfile/encodingx"
)

const encodingDetectSampleSize = 1024 * 1024

// ConvertEncodingFile rewrites a text file into the requested target encoding
// while preserving Novera's safe temp-output and optional swap workflow.
func ConvertEncodingFile(ctx context.Context, sourcePath string, outputPath string, target string, opts FileOptions) (FileSummary, error) {
	if opts.SwapOriginal {
		return FileSummary{}, ErrSwapOriginalDisabled
	}
	same, err := samePath(sourcePath, outputPath)
	if err != nil {
		return FileSummary{}, err
	}
	if same {
		return FileSummary{}, errors.New("output path must be different from source path")
	}

	targetName, err := normalizeTargetEncoding(target)
	if err != nil {
		return FileSummary{}, err
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

	sample, err := readFileSample(src, st.Size(), encodingDetectSampleSize)
	if err != nil {
		return summary, err
	}
	detected := encodingx.DetectSample(sample)
	bomLen := int64(len(encodingx.BOMBytes(detected.Name)))
	if detected.HasBOM {
		if _, err := src.Seek(bomLen, io.SeekStart); err != nil {
			return summary, err
		}
	} else if _, err := src.Seek(0, io.SeekStart); err != nil {
		return summary, err
	}

	dst, err := openExclusive(summary.TempPath)
	if err != nil {
		return summary, err
	}

	manifest := Manifest{
		Operation:     "encoding-convert",
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

	progressReader := &progressReader{
		reader:    src,
		total:     st.Size(),
		processed: bomLen,
		report: func(processed int64) {
			manifest.BytesProcessed = processed
			if opts.Progress != nil {
				opts.Progress(Progress{
					BytesProcessed: processed,
					BytesTotal:     st.Size(),
				})
			}
		},
	}
	decodedReader, err := encodingx.NewDecoderReader(detected.Name, progressReader)
	if err != nil {
		_ = dst.Close()
		_ = removePath(summary.TempPath)
		return summary, err
	}

	if shouldWriteEncodingBOM(detected, targetName) {
		if _, err := dst.Write(encodingx.BOMBytes(targetName)); err != nil {
			_ = dst.Close()
			_ = removePath(summary.TempPath)
			return summary, err
		}
	}

	encodedWriter, err := encodingx.NewEncoderWriter(targetName, dst)
	if err != nil {
		_ = dst.Close()
		_ = removePath(summary.TempPath)
		return summary, err
	}
	writerIsDestination := false
	if direct, ok := encodedWriter.(*os.File); ok && direct == dst {
		writerIsDestination = true
	}

	copyErr := copyTranscoded(ctx, decodedReader, encodedWriter)
	closeErr, _ := closeWriter(encodedWriter)
	dstCloseErr := error(nil)
	if !writerIsDestination {
		dstCloseErr = dst.Close()
	}

	if copyErr != nil {
		failFileTransformWrite(summary, &manifest, opts, copyErr)
		return summary, copyErr
	}
	if closeErr != nil {
		writeFailedManifest(summary.ManifestPath, &manifest, closeErr)
		return summary, closeErr
	}
	if dstCloseErr != nil {
		writeFailedManifest(summary.ManifestPath, &manifest, dstCloseErr)
		return summary, dstCloseErr
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

	return summary, nil
}

func copyTranscoded(ctx context.Context, src io.Reader, dst io.Writer) error {
	buf := make([]byte, 64*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := src.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func normalizeTargetEncoding(target string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(target)) {
	case "UTF-8":
		return "UTF-8", nil
	case "UTF-16LE":
		return "UTF-16LE", nil
	case "UTF-16BE":
		return "UTF-16BE", nil
	case "WINDOWS-1251":
		return "Windows-1251", nil
	case "WINDOWS-1252":
		return "Windows-1252", nil
	default:
		return "", errors.New("unsupported encoding: " + target)
	}
}

func shouldWriteEncodingBOM(source encodingx.Info, target string) bool {
	switch target {
	case "UTF-16LE", "UTF-16BE":
		return true
	case "UTF-8":
		return source.HasBOM && strings.EqualFold(source.Name, "UTF-8")
	default:
		return false
	}
}
