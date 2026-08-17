package exportx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/fileio"
)

func ExportVisibleText(ctx context.Context, outputPath string, text string, encodingName string) (Summary, error) {
	return ExportVisibleTextWithOptions(ctx, outputPath, text, encodingName, Options{})
}

func ExportVisibleTextWithOptions(ctx context.Context, outputPath string, text string, encodingName string, opts Options) (_ Summary, retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := fileio.ValidateExactOutputPath(outputPath); err != nil {
		return Summary{}, err
	}

	select {
	case <-ctx.Done():
		return Summary{}, ctx.Err()
	default:
	}

	encoded, err := encodingx.EncodeString(encodingName, text)
	if err != nil {
		return Summary{}, err
	}
	data := append([]byte{}, targetBOM(encodingName)...)
	data = append(data, encoded...)
	summary := Summary{
		OutputPath:     outputPath,
		BytesWritten:   int64(len(data)),
		Mode:           "visible-selection",
		SourceEncoding: encodingName,
		TargetEncoding: encodingName,
	}
	if opts.ComputeSHA256 {
		sum := sha256.Sum256(data)
		summary.ChecksumAlgorithm = "sha256"
		summary.SHA256 = hex.EncodeToString(sum[:])
	}
	if shouldWriteExportManifest(opts) {
		summary.ManifestPath = exportManifestPathFor(outputPath, opts.ManifestPath)
		if err := ensureExportManifestAvailable(summary.ManifestPath, outputPath); err != nil {
			return summary, err
		}
	}

	dst, err := openCreatedOutput(outputPath)
	if err != nil {
		return summary, err
	}
	defer func() { retErr = errors.Join(retErr, dst.Cleanup()) }()
	if n, err := dst.Write(data); err != nil {
		return summary, err
	} else if n != len(data) {
		return summary, io.ErrShortWrite
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
