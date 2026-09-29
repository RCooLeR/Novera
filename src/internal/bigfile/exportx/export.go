package exportx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/fileio"
)

type Options struct {
	Progress      func(done int64, total int64)
	ComputeSHA256 bool
	WriteManifest bool
	ManifestPath  string
	// ValidateSource runs after the complete output has been synced and
	// immediately before its no-clobber publication.
	ValidateSource func(context.Context) error
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

func exportByteRangeCore(ctx context.Context, doc document.ReaderAtSize, sourcePath string, outputPath string, start int64, end int64, mode string, startLine int64, endLine int64, usedLineRange bool, opts Options) (_ Summary, retErr error) {
	if doc == nil {
		return Summary{}, errors.New("document is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := fileio.ValidateExactOutputPath(outputPath); err != nil {
		return Summary{}, err
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
		if err := ensureExportManifestAvailable(summary.ManifestPath, sourcePath, outputPath); err != nil {
			return summary, err
		}
	}

	if err := ctx.Err(); err != nil {
		return summary, err
	}
	dst, err := openCreatedOutput(outputPath, sourcePath)
	if err != nil {
		return summary, err
	}
	defer func() { retErr = errors.Join(retErr, dst.Cleanup()) }()

	total := end - start
	reader := io.NewSectionReader(doc, start, total)
	buf := make([]byte, 1024*1024)
	var written int64
	var checksum hashWriter
	if opts.ComputeSHA256 {
		checksum = sha256.New()
	}

	for written < total {
		if err := ctx.Err(); err != nil {
			return summary, err
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
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return summary, readErr
		}
		if written == total {
			break
		}
		if errors.Is(readErr, io.EOF) {
			return summary, io.ErrUnexpectedEOF
		}
		if n == 0 {
			return summary, io.ErrNoProgress
		}
	}
	if written != total {
		return summary, io.ErrUnexpectedEOF
	}

	summary.BytesWritten = written
	if checksum != nil {
		summary.SHA256 = hex.EncodeToString(checksum.Sum(nil))
	}
	var commitErr error
	if opts.ValidateSource != nil {
		commitErr = dst.CommitContextValidated(ctx, opts.ValidateSource)
	} else {
		commitErr = dst.CommitContext(ctx)
	}
	if commitErr != nil {
		return summary, commitErr
	}
	if shouldWriteExportManifest(opts) {
		if err := writeExportManifest(ctx, summary); err != nil {
			return summary, exportManifestPublicationError(summary, err)
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
