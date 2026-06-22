package exportx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/fileio"
)

type Options struct {
	Progress      func(done int64, total int64)
	ComputeSHA256 bool
	WriteManifest bool
	ManifestPath  string
}

type Summary struct {
	SourcePath        string
	OutputPath        string
	ManifestPath      string
	StartOffset       int64
	EndOffset         int64
	BytesWritten      int64
	StartLine         int64
	EndLine           int64
	Mode              string
	UsedLineRange     bool
	SourceEncoding    string
	TargetEncoding    string
	ChecksumAlgorithm string
	SHA256            string
}

func ExportByteRange(ctx context.Context, doc document.ReaderAtSize, sourcePath string, outputPath string, start int64, end int64, opts Options) (Summary, error) {
	return exportByteRangeCore(ctx, doc, sourcePath, outputPath, start, end, "byte-range", 0, 0, false, opts)
}

func ExportVisibleRange(ctx context.Context, doc document.ReaderAtSize, sourcePath string, outputPath string, start int64, end int64, opts Options) (Summary, error) {
	return exportByteRangeCore(ctx, doc, sourcePath, outputPath, start, end, "visible-range", 0, 0, false, opts)
}

func ExportLineRange(ctx context.Context, doc *document.FileDocument, sourcePath string, outputPath string, startLine int64, endLine int64, opts Options) (Summary, error) {
	if doc == nil {
		return Summary{}, errors.New("document is required")
	}
	startOffset, endOffset, err := doc.LineRangeOffsets(startLine, endLine)
	if err != nil {
		return Summary{}, err
	}
	return exportByteRangeCore(ctx, doc, sourcePath, outputPath, startOffset, endOffset, "line-range", startLine, endLine, true, opts)
}

func exportByteRangeCore(ctx context.Context, doc document.ReaderAtSize, sourcePath string, outputPath string, start int64, end int64, mode string, startLine int64, endLine int64, usedLineRange bool, opts Options) (Summary, error) {
	if doc == nil {
		return Summary{}, errors.New("document is required")
	}
	if outputPath == "" {
		return Summary{}, errors.New("output path is required")
	}
	if start < 0 {
		start = 0
	}
	size := doc.Size()
	if end <= 0 || end > size {
		end = size
	}
	if end < start {
		return Summary{}, errors.New("end offset must be greater than or equal to start offset")
	}
	if same, err := samePath(sourcePath, outputPath); err != nil {
		return Summary{}, err
	} else if same {
		return Summary{}, errors.New("output path must be different from source path")
	}
	if _, err := os.Stat(outputPath); err == nil {
		return Summary{}, errors.New("output file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Summary{}, err
	}
	summary := Summary{
		SourcePath:    sourcePath,
		OutputPath:    outputPath,
		StartOffset:   start,
		EndOffset:     end,
		StartLine:     startLine,
		EndLine:       endLine,
		Mode:          mode,
		UsedLineRange: usedLineRange,
	}
	if opts.ComputeSHA256 {
		summary.ChecksumAlgorithm = "sha256"
	}
	if shouldWriteExportManifest(opts) {
		summary.ManifestPath = exportManifestPathFor(outputPath, opts.ManifestPath)
		if err := ensureExportManifestAvailable(summary.ManifestPath); err != nil {
			return summary, err
		}
	}

	dst, err := openCreatedOutput(outputPath)
	if err != nil {
		return summary, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = dst.Cleanup()
		}
	}()

	total := end - start
	reader := io.NewSectionReader(doc, start, total)
	buf := make([]byte, 1024*1024)
	var written int64
	var checksum hashWriter
	if opts.ComputeSHA256 {
		checksum = sha256.New()
	}

	for {
		select {
		case <-ctx.Done():
			return summary, ctx.Err()
		default:
		}

		n, readErr := reader.Read(buf)
		if n > 0 {
			w, writeErr := dst.Write(buf[:n])
			written += int64(w)
			if checksum != nil && w > 0 {
				_, _ = checksum.Write(buf[:w])
			}
			if opts.Progress != nil {
				opts.Progress(written, total)
			}
			if writeErr != nil {
				return summary, writeErr
			}
			if w != n {
				return summary, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return summary, readErr
		}
	}
	if err := dst.Sync(); err != nil {
		return summary, err
	}
	if err := dst.Close(); err != nil {
		return summary, err
	}
	cleanup = false

	summary.BytesWritten = written
	if checksum != nil {
		summary.SHA256 = hex.EncodeToString(checksum.Sum(nil))
	}
	if shouldWriteExportManifest(opts) {
		if err := writeExportManifest(summary); err != nil {
			return summary, cleanupOutputAfterManifestFailure(outputPath, err)
		}
	}
	return summary, nil
}

func samePath(a string, b string) (bool, error) {
	return fileio.SamePath(a, b)
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}
