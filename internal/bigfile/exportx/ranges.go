package exportx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"

	"novera/internal/bigfile/document"
)

// ExportByteRanges concatenates validated, ordered, non-overlapping source
// ranges into one atomically published output. It exists for noncontiguous SQL
// table blocks; bytes in the gaps are deliberately not copied.
func ExportByteRanges(ctx context.Context, doc document.ReaderAtSize, sourcePath, outputPath string, ranges [][2]int64, opts Options) (_ Summary, retErr error) {
	if doc == nil {
		return Summary{}, errors.New("document is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if outputPath == "" {
		return Summary{}, errors.New("output path is required")
	}
	if shouldWriteExportManifest(opts) {
		return Summary{}, errors.New("multi-range export manifest is unsupported without explicit range metadata")
	}
	if len(ranges) == 0 {
		return Summary{}, errors.New("at least one byte range is required")
	}
	size := doc.Size()
	var total int64
	previousEnd := int64(-1)
	for index, current := range ranges {
		start, end := current[0], current[1]
		if start < 0 || end <= start || end > size {
			return Summary{}, fmt.Errorf("invalid byte range %d [%d,%d) for source size %d", index, start, end, size)
		}
		if previousEnd > start {
			return Summary{}, fmt.Errorf("byte range %d overlaps or is out of order", index)
		}
		length := end - start
		if total > math.MaxInt64-length {
			return Summary{}, errors.New("byte range total overflows int64")
		}
		total += length
		previousEnd = end
	}
	summary := Summary{
		SourcePath:  sourcePath,
		OutputPath:  outputPath,
		StartOffset: ranges[0][0],
		EndOffset:   ranges[len(ranges)-1][1],
		Mode:        "byte-ranges",
	}
	if opts.ComputeSHA256 {
		summary.ChecksumAlgorithm = "sha256"
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	destination, err := openCreatedOutput(outputPath, sourcePath)
	if err != nil {
		return summary, err
	}
	defer func() { retErr = errors.Join(retErr, destination.Cleanup()) }()

	var checksum hashWriter
	if opts.ComputeSHA256 {
		checksum = sha256.New()
	}
	var written int64
	for _, current := range ranges {
		base := written
		part, copyErr := copyExactRawRange(ctx, doc, current[0], current[1]-current[0], destination, func(done, _ int64) {
			if opts.Progress != nil {
				opts.Progress(base+done, total)
			}
		}, checksum)
		written += part
		summary.BytesWritten = written
		if copyErr != nil {
			return summary, copyErr
		}
	}
	if written != total {
		return summary, fmt.Errorf("multi-range export wrote %d of %d bytes", written, total)
	}
	if checksum != nil {
		summary.SHA256 = hex.EncodeToString(checksum.Sum(nil))
	}
	if opts.ValidateSource != nil {
		err = destination.CommitContextValidated(ctx, opts.ValidateSource)
	} else {
		err = destination.CommitContext(ctx)
	}
	if err != nil {
		return summary, err
	}
	return summary, nil
}

func copyExactRawRange(
	ctx context.Context,
	doc document.ReaderAtSize,
	start int64,
	total int64,
	dst io.Writer,
	progress func(done int64, total int64),
	checksum hashWriter,
) (int64, error) {
	reader := io.NewSectionReader(doc, start, total)
	buf := make([]byte, 1024*1024)
	var written int64
	for written < total {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := reader.Read(buf)
		if n > 0 {
			w, writeErr := dst.Write(buf[:n])
			written += int64(w)
			if checksum != nil && w > 0 {
				_, _ = checksum.Write(buf[:w])
			}
			if progress != nil {
				progress(written, total)
			}
			if writeErr != nil {
				return written, writeErr
			}
			if w != n {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return written, readErr
		}
		if written == total {
			break
		}
		if errors.Is(readErr, io.EOF) {
			return written, io.ErrUnexpectedEOF
		}
		if n == 0 {
			return written, io.ErrNoProgress
		}
	}
	if written != total {
		return written, io.ErrUnexpectedEOF
	}
	return written, nil
}
