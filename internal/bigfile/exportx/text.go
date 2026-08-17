package exportx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/fileio"
)

func ExportByteRangeText(ctx context.Context, doc document.ReaderAtSize, sourcePath string, outputPath string, start int64, end int64, sourceEncoding string, targetEncoding string, opts Options) (Summary, error) {
	return exportByteRangeTextCore(ctx, doc, sourcePath, outputPath, start, end, sourceEncoding, targetEncoding, "byte-range-text", 0, 0, false, opts)
}

func ExportVisibleRangeText(ctx context.Context, doc document.ReaderAtSize, sourcePath string, outputPath string, start int64, end int64, sourceEncoding string, targetEncoding string, opts Options) (Summary, error) {
	return exportByteRangeTextCore(ctx, doc, sourcePath, outputPath, start, end, sourceEncoding, targetEncoding, "visible-range-text", 0, 0, false, opts)
}

func ExportLineRangeText(ctx context.Context, doc *document.FileDocument, sourcePath string, outputPath string, startLine int64, endLine int64, sourceEncoding string, targetEncoding string, opts Options) (Summary, error) {
	if doc == nil {
		return Summary{}, errors.New("document is required")
	}
	startOffset, endOffset, err := doc.LineRangeOffsets(startLine, endLine)
	if err != nil {
		return Summary{}, err
	}
	return exportByteRangeTextCore(ctx, doc, sourcePath, outputPath, startOffset, endOffset, sourceEncoding, targetEncoding, "line-range-text", startLine, endLine, true, opts)
}

func exportByteRangeTextCore(ctx context.Context, doc document.ReaderAtSize, sourcePath string, outputPath string, start int64, end int64, sourceEncoding string, targetEncoding string, mode string, startLine int64, endLine int64, usedLineRange bool, opts Options) (_ Summary, retErr error) {
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
	sourceEncoding = strings.TrimSpace(sourceEncoding)
	if sourceEncoding == "" {
		sourceEncoding = "UTF-8"
	}
	targetEncoding = strings.TrimSpace(targetEncoding)
	if targetEncoding == "" {
		targetEncoding = sourceEncoding
	}

	start, end = stripLeadingSourceBOM(doc, start, end, sourceEncoding)
	total := end - start
	if total < 0 {
		total = 0
	}
	summary := Summary{
		SourcePath:     sourcePath,
		OutputPath:     outputPath,
		StartOffset:    start,
		EndOffset:      end,
		StartLine:      startLine,
		EndLine:        endLine,
		Mode:           mode,
		UsedLineRange:  usedLineRange,
		SourceEncoding: sourceEncoding,
		TargetEncoding: targetEncoding,
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

	reader := io.NewSectionReader(doc, start, total)
	progressReader := &progressRangeReader{
		reader: reader,
		total:  total,
		report: opts.Progress,
	}
	decodedReader, err := encodingx.NewDecoderReader(sourceEncoding, progressReader)
	if err != nil {
		return summary, err
	}

	var checksum hashWriter
	if opts.ComputeSHA256 {
		checksum = sha256.New()
	}
	countedDst := &countingWriter{w: dst, checksum: checksum}
	if bom := targetBOM(targetEncoding); len(bom) > 0 {
		if _, err := countedDst.Write(bom); err != nil {
			return summary, err
		}
	}
	encodedWriter, err := encodingx.NewEncoderWriter(targetEncoding, countedDst)
	if err != nil {
		return summary, err
	}

	if err := copyTextRange(ctx, decodedReader, encodedWriter); err != nil {
		return summary, err
	}
	if progressReader.done != total {
		return summary, io.ErrUnexpectedEOF
	}
	if closer, ok := encodedWriter.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			return summary, err
		}
	}
	summary.BytesWritten = countedDst.written
	if checksum != nil {
		summary.SHA256 = hex.EncodeToString(checksum.Sum(nil))
	}
	if err := dst.CommitContext(ctx); err != nil {
		return summary, err
	}
	if shouldWriteExportManifest(opts) {
		if err := writeExportManifest(ctx, summary); err != nil {
			return summary, exportManifestPublicationError(summary, err)
		}
	}
	return summary, nil
}

type progressRangeReader struct {
	reader io.Reader
	total  int64
	done   int64
	report func(done int64, total int64)
}

func (p *progressRangeReader) Read(buf []byte) (int, error) {
	n, err := p.reader.Read(buf)
	if n > 0 {
		p.done += int64(n)
		if p.report != nil {
			p.report(p.done, p.total)
		}
	}
	return n, err
}

type countingWriter struct {
	w        io.Writer
	checksum hashWriter
	written  int64
}

func (c *countingWriter) Write(buf []byte) (int, error) {
	n, err := c.w.Write(buf)
	c.written += int64(n)
	if c.checksum != nil && n > 0 {
		_, _ = c.checksum.Write(buf[:n])
	}
	return n, err
}

func copyTextRange(ctx context.Context, src io.Reader, dst io.Writer) error {
	buf := make([]byte, 64*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written, err := dst.Write(buf[:n])
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
}

func stripLeadingSourceBOM(doc document.ReaderAtSize, start int64, end int64, sourceEncoding string) (int64, int64) {
	if start != 0 || end <= 0 {
		return start, end
	}
	bom := encodingx.BOMBytes(sourceEncoding)
	if len(bom) == 0 {
		return start, end
	}
	got := make([]byte, len(bom))
	n, err := doc.ReadAt(got, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return start, end
	}
	if n < len(bom) || !bytes.Equal(got[:n], bom[:n]) {
		return start, end
	}
	shift := int64(len(bom))
	if shift >= end {
		return end, end
	}
	return start + shift, end
}

func targetBOM(encodingName string) []byte {
	switch strings.ToUpper(strings.TrimSpace(encodingName)) {
	case "UTF-16LE", "UTF-16BE":
		return encodingx.BOMBytes(encodingName)
	default:
		return nil
	}
}
