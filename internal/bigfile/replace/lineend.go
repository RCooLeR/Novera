package replace

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"novera/internal/bigfile/encodingx"
)

const lineEndingDetectSampleSize = 1024 * 1024

// ConvertLineEndingsFile rewrites a text file with the requested line-ending style
// while preserving the detected encoding and the original-only safety model.
func ConvertLineEndingsFile(ctx context.Context, sourcePath string, outputPath string, target string, opts FileOptions) (FileSummary, error) {
	same, err := samePath(sourcePath, outputPath)
	if err != nil {
		return FileSummary{}, err
	}
	if same {
		return FileSummary{}, errors.New("output path must be different from source path")
	}

	targetBytes, err := lineEndingBytes(target)
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

	sample, err := readFileSample(src, st.Size(), lineEndingDetectSampleSize)
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
		Operation:     "line-ending-convert",
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
					Matches:        manifest.Matches,
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

	if detected.HasBOM {
		if _, err := dst.Write(encodingx.BOMBytes(detected.Name)); err != nil {
			_ = dst.Close()
			_ = removePath(summary.TempPath)
			return summary, err
		}
	}

	encodedWriter, err := encodingx.NewEncoderWriter(detected.Name, dst)
	if err != nil {
		_ = dst.Close()
		_ = removePath(summary.TempPath)
		return summary, err
	}
	writerIsDestination := false
	if direct, ok := encodedWriter.(*os.File); ok && direct == dst {
		writerIsDestination = true
	}

	conversions, convertErr := rewriteLineEndings(ctx, decodedReader, encodedWriter, targetBytes, func(converted int64) {
		manifest.Matches = converted
		if opts.Progress != nil {
			opts.Progress(Progress{
				BytesProcessed: manifest.BytesProcessed,
				BytesTotal:     st.Size(),
				Matches:        converted,
			})
		}
	})
	closeErr, _ := closeWriter(encodedWriter)
	dstCloseErr := error(nil)
	if !writerIsDestination {
		dstCloseErr = dst.Close()
	}

	if convertErr != nil {
		failFileTransformWrite(summary, &manifest, opts, convertErr)
		return summary, convertErr
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

	manifest.Matches = conversions
	if err := writeCompletedManifestOrFail(summary.ManifestPath, &manifest, st.Size()); err != nil {
		return summary, err
	}

	summary.Matches = conversions
	return summary, nil
}

type progressReader struct {
	reader    io.Reader
	total     int64
	processed int64
	report    func(processed int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.processed += int64(n)
		if r.report != nil {
			r.report(r.processed)
		}
	}
	return n, err
}

func rewriteLineEndings(ctx context.Context, src io.Reader, dst io.Writer, target []byte, onConvert func(int64)) (int64, error) {
	buf := make([]byte, 64*1024)
	out := make([]byte, 0, len(buf)+len(target))
	var conversions int64
	pendingCR := false

	flushOut := func() error {
		if len(out) == 0 {
			return nil
		}
		if _, err := dst.Write(out); err != nil {
			return err
		}
		out = out[:0]
		return nil
	}

	emitTarget := func() {
		out = append(out, target...)
		conversions++
		if onConvert != nil {
			onConvert(conversions)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return conversions, ctx.Err()
		default:
		}

		n, err := src.Read(buf)
		if n > 0 {
			for _, b := range buf[:n] {
				if pendingCR {
					if b == '\n' {
						emitTarget()
						pendingCR = false
						continue
					}
					emitTarget()
					pendingCR = false
				}
				switch b {
				case '\r':
					pendingCR = true
				case '\n':
					emitTarget()
				default:
					out = append(out, b)
				}
			}
			if len(out) >= cap(buf) {
				if err := flushOut(); err != nil {
					return conversions, err
				}
			}
		}

		if errors.Is(err, io.EOF) {
			if pendingCR {
				emitTarget()
			}
			if err := flushOut(); err != nil {
				return conversions, err
			}
			return conversions, nil
		}
		if err != nil {
			return conversions, err
		}
	}
}

func lineEndingBytes(target string) ([]byte, error) {
	switch target {
	case "LF":
		return []byte("\n"), nil
	case "CRLF":
		return []byte("\r\n"), nil
	case "CR":
		return []byte("\r"), nil
	default:
		return nil, errors.New("unsupported line ending: " + target)
	}
}

func closeWriter(w io.Writer) (error, bool) {
	if closer, ok := w.(interface{ Close() error }); ok {
		return closer.Close(), true
	}
	return nil, false
}

func readFileSample(f *os.File, size int64, max int64) ([]byte, error) {
	if size <= 0 {
		return nil, nil
	}
	if size < max {
		max = size
	}
	buf := make([]byte, max)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}
