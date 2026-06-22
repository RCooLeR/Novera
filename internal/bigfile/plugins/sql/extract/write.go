package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/exportx"
	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/plugins/sql/analyze"
)

const defaultManifestName = "quarry-sql-extract-manifest.json"

type WriteOptions struct {
	PlanOptions
	ManifestPath  string
	ComputeSHA256 bool
	Progress      func(done int64, total int64, outputs int)
}

type WriteSummary struct {
	Operation         string
	SourcePath        string
	SourceSize        int64
	ManifestPath      string
	Outputs           []TableRange
	BytesWritten      int64
	ChecksumAlgorithm string   `json:",omitempty"`
	HeaderIncluded    bool     // false: slices omit the dump preamble (see Note)
	DetectedCharsets  []string `json:",omitempty"`
	Note              string   `json:",omitempty"`
}

func SplitByTable(ctx context.Context, doc document.ReaderAtSize, sourcePath string, summary analyze.Summary, opts WriteOptions) (WriteSummary, error) {
	preview, err := SplitByTablePreview(summary, sizeOf(doc), opts.PlanOptions)
	if err != nil {
		return WriteSummary{}, err
	}
	return writePreview(ctx, doc, sourcePath, preview, opts)
}

func ExtractTable(ctx context.Context, doc document.ReaderAtSize, sourcePath string, summary analyze.Summary, tableName string, opts WriteOptions) (WriteSummary, error) {
	preview, err := ExtractTablePreview(summary, sizeOf(doc), tableName, opts.PlanOptions)
	if err != nil {
		return WriteSummary{}, err
	}
	return writePreview(ctx, doc, sourcePath, preview, opts)
}

func writePreview(ctx context.Context, doc document.ReaderAtSize, sourcePath string, preview ManifestPreview, opts WriteOptions) (WriteSummary, error) {
	if doc == nil {
		return WriteSummary{}, errors.New("document is required")
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		return WriteSummary{}, errors.New("output directory is required")
	}
	if len(preview.Tables) == 0 {
		return WriteSummary{}, errors.New("no SQL table ranges to write")
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return WriteSummary{}, err
	}
	manifestPath := strings.TrimSpace(opts.ManifestPath)
	if manifestPath == "" {
		manifestPath = filepath.Join(opts.OutputDir, defaultManifestName)
	}
	if err := ensureManifestAvailable(manifestPath); err != nil {
		return WriteSummary{}, err
	}

	total := previewTotalBytes(preview)
	summary := WriteSummary{
		Operation:        preview.Operation,
		SourcePath:       sourcePath,
		SourceSize:       preview.SourceSize,
		ManifestPath:     manifestPath,
		HeaderIncluded:   preview.HeaderIncluded,
		DetectedCharsets: preview.DetectedCharsets,
		Note:             preview.Note,
	}
	if opts.ComputeSHA256 {
		summary.ChecksumAlgorithm = "sha256"
	}
	done := int64(0)
	for i, table := range preview.Tables {
		if strings.TrimSpace(table.OutputPath) == "" {
			return failWrite(summary, fmt.Errorf("table %q has no output path", table.Name))
		}
		baseDone := done
		part, err := exportx.ExportByteRange(ctx, doc, sourcePath, table.OutputPath, table.StartOffset, table.EndOffset, exportx.Options{
			ComputeSHA256: opts.ComputeSHA256,
			Progress: func(written int64, partTotal int64) {
				if opts.Progress != nil {
					opts.Progress(baseDone+written, total, i+1)
				}
			},
		})
		if err != nil {
			return failWrite(summary, err)
		}
		table.Bytes = part.BytesWritten
		table.SHA256 = part.SHA256
		summary.Outputs = append(summary.Outputs, table)
		done += part.BytesWritten
		summary.BytesWritten += part.BytesWritten
		if opts.Progress != nil {
			opts.Progress(done, total, len(summary.Outputs))
		}
	}
	if err := writeManifest(summary); err != nil {
		return failWrite(summary, err)
	}
	return summary, nil
}

func writeManifest(summary WriteSummary) error {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	_, err = fileio.WriteFileAtomic(summary.ManifestPath, append(data, '\n'), fileio.AtomicWriteOptions{Mode: 0o600})
	return err
}

func ensureManifestAvailable(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("manifest path is required")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", fileio.ErrExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(path + ".quarry.tmp"); err == nil {
		return fmt.Errorf("%w: %s", fileio.ErrTempExists, path+".quarry.tmp")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func failWrite(summary WriteSummary, err error) (WriteSummary, error) {
	if cleanupErr := cleanupOutputs(summary.Outputs); cleanupErr != nil {
		return summary, errors.Join(err, cleanupErr)
	}
	return summary, err
}

func cleanupOutputs(outputs []TableRange) error {
	var cleanupErr error
	for _, output := range outputs {
		if strings.TrimSpace(output.OutputPath) == "" {
			continue
		}
		if err := os.Remove(output.OutputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove SQL extract output %q: %w", output.OutputPath, err))
		}
	}
	return cleanupErr
}

func previewTotalBytes(preview ManifestPreview) int64 {
	var total int64
	for _, table := range preview.Tables {
		total += table.Bytes
	}
	return total
}

func sizeOf(doc document.ReaderAtSize) int64 {
	if doc == nil {
		return 0
	}
	return doc.Size()
}
