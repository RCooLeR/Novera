package exportx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"novera/internal/bigfile/encodingx"
)

func ExportVisibleText(ctx context.Context, outputPath string, text string, encodingName string) (Summary, error) {
	return ExportVisibleTextWithOptions(ctx, outputPath, text, encodingName, Options{})
}

func ExportVisibleTextWithOptions(ctx context.Context, outputPath string, text string, encodingName string, opts Options) (Summary, error) {
	if outputPath == "" {
		return Summary{}, errors.New("output path is required")
	}
	if _, err := os.Stat(outputPath); err == nil {
		return Summary{}, errors.New("output file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
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
	if n, err := dst.Write(data); err != nil {
		return summary, err
	} else if n != len(data) {
		return summary, io.ErrShortWrite
	}
	if err := dst.Sync(); err != nil {
		return summary, err
	}
	if err := dst.Close(); err != nil {
		return summary, err
	}
	cleanup = false

	if shouldWriteExportManifest(opts) {
		if err := writeExportManifest(summary); err != nil {
			return summary, cleanupOutputAfterManifestFailure(outputPath, err)
		}
	}
	return summary, nil
}
