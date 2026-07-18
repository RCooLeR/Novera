package exportx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/fileio"
)

type SplitOptions struct {
	Progress      func(done int64, total int64, parts int)
	ComputeSHA256 bool
	ManifestPath  string
}

// Split operations treat generated part files as an all-or-cleanup set. If a
// split is canceled or fails after writing earlier parts, Novera removes only
// the part files created by the current operation and leaves the source file and
// any pre-existing conflicting outputs untouched.
type SplitSummary struct {
	BasePath          string
	Mode              string
	Outputs           []string
	OutputChecksums   []OutputChecksum
	ManifestPath      string
	BytesWritten      int64
	Parts             int
	BytesPerPart      int64
	LinesPerPart      int64
	ChecksumAlgorithm string
}

type OutputChecksum struct {
	Path   string
	SHA256 string
}

const defaultSplitManifestSuffix = ".quarry-split-manifest.json"

func SplitBySize(ctx context.Context, doc document.ReaderAtSize, sourcePath string, outputBasePath string, bytesPerPart int64, opts SplitOptions) (SplitSummary, error) {
	if doc == nil {
		return SplitSummary{}, errors.New("document is required")
	}
	if outputBasePath == "" {
		return SplitSummary{}, errors.New("output base path is required")
	}
	if bytesPerPart <= 0 {
		return SplitSummary{}, errors.New("bytes per part must be positive")
	}

	total := doc.Size()
	summary := SplitSummary{
		BasePath:     outputBasePath,
		Mode:         "split-size",
		ManifestPath: splitManifestPathFor(outputBasePath, opts.ManifestPath),
		BytesPerPart: bytesPerPart,
	}
	if opts.ComputeSHA256 {
		summary.ChecksumAlgorithm = "sha256"
	}
	if err := ensureSplitManifestAvailable(summary.ManifestPath); err != nil {
		return summary, err
	}
	if total == 0 {
		return summary, writeSplitManifest(summary)
	}

	partCount := int(math.Ceil(float64(total) / float64(bytesPerPart)))
	done := int64(0)
	for i := 0; i < partCount; i++ {
		start := int64(i) * bytesPerPart
		end := start + bytesPerPart
		if end > total {
			end = total
		}
		outputPath := partPath(outputBasePath, i+1)
		baseDone := done
		partSummary, err := ExportByteRange(ctx, doc, sourcePath, outputPath, start, end, Options{
			ComputeSHA256: opts.ComputeSHA256,
			Progress: func(written int64, partTotal int64) {
				if opts.Progress != nil {
					opts.Progress(baseDone+written, total, i+1)
				}
			},
		})
		if err != nil {
			return failSplit(summary, err)
		}
		summary.Outputs = append(summary.Outputs, outputPath)
		if opts.ComputeSHA256 {
			summary.OutputChecksums = append(summary.OutputChecksums, OutputChecksum{Path: outputPath, SHA256: partSummary.SHA256})
		}
		done += partSummary.BytesWritten
		summary.BytesWritten += partSummary.BytesWritten
		summary.Parts = len(summary.Outputs)
		if opts.Progress != nil {
			opts.Progress(done, total, summary.Parts)
		}
	}

	if err := writeSplitManifest(summary); err != nil {
		return failSplit(summary, err)
	}
	return summary, nil
}

func SplitByLineCount(ctx context.Context, doc *document.FileDocument, sourcePath string, outputBasePath string, linesPerPart int64, opts SplitOptions) (SplitSummary, error) {
	if doc == nil {
		return SplitSummary{}, errors.New("document is required")
	}
	if outputBasePath == "" {
		return SplitSummary{}, errors.New("output base path is required")
	}
	if linesPerPart <= 0 {
		return SplitSummary{}, errors.New("lines per part must be positive")
	}

	total := doc.Size()
	summary := SplitSummary{
		BasePath:     outputBasePath,
		Mode:         "split-lines",
		ManifestPath: splitManifestPathFor(outputBasePath, opts.ManifestPath),
		LinesPerPart: linesPerPart,
	}
	if opts.ComputeSHA256 {
		summary.ChecksumAlgorithm = "sha256"
	}
	if err := ensureSplitManifestAvailable(summary.ManifestPath); err != nil {
		return summary, err
	}
	if total == 0 {
		return summary, writeSplitManifest(summary)
	}

	// Carry the running byte offset forward instead of re-resolving the part's
	// start line each iteration. Each part's end is the start of the line after
	// its last line (a single line-offset lookup); the next part starts exactly
	// there. This avoids the redundant double resolution and the per-part rescan
	// from a stale index frontier on a not-yet-fully-indexed huge file.
	startLine := int64(1)
	startOffset, ok, err := doc.LineStartOffset(startLine)
	if err != nil {
		return failSplit(summary, err)
	}
	if !ok {
		return summary, writeSplitManifest(summary)
	}
	done := int64(0)
	for part := 1; ; part++ {
		if ctx.Err() != nil {
			return failSplit(summary, ctx.Err())
		}

		nextStartLine := startLine + linesPerPart
		endOffset, ok, err := doc.LineStartOffset(nextStartLine)
		if err != nil {
			return failSplit(summary, err)
		}
		if !ok || endOffset > total {
			endOffset = total
		}
		if endOffset <= startOffset {
			break
		}

		outputPath := partPath(outputBasePath, part)
		baseDone := done
		partSummary, err := ExportByteRange(ctx, doc, sourcePath, outputPath, startOffset, endOffset, Options{
			ComputeSHA256: opts.ComputeSHA256,
			Progress: func(written int64, partTotal int64) {
				if opts.Progress != nil {
					opts.Progress(baseDone+written, total, part)
				}
			},
		})
		if err != nil {
			return failSplit(summary, err)
		}
		summary.Outputs = append(summary.Outputs, outputPath)
		if opts.ComputeSHA256 {
			summary.OutputChecksums = append(summary.OutputChecksums, OutputChecksum{Path: outputPath, SHA256: partSummary.SHA256})
		}
		done += partSummary.BytesWritten
		summary.BytesWritten += partSummary.BytesWritten
		summary.Parts = len(summary.Outputs)
		if opts.Progress != nil {
			opts.Progress(done, total, summary.Parts)
		}

		if endOffset >= total {
			break
		}
		startOffset = endOffset
		startLine = nextStartLine
	}

	if err := writeSplitManifest(summary); err != nil {
		return failSplit(summary, err)
	}
	return summary, nil
}

func splitManifestPathFor(basePath string, override string) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	return basePath + defaultSplitManifestSuffix
}

func ensureSplitManifestAvailable(path string) error {
	return ensureManifestAvailable("split", path)
}

func writeSplitManifest(summary SplitSummary) error {
	if strings.TrimSpace(summary.ManifestPath) == "" {
		return errors.New("split manifest path is required")
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = fileio.WriteFileAtomic(summary.ManifestPath, data, fileio.AtomicWriteOptions{Mode: 0o600})
	return err
}

func partPath(basePath string, part int) string {
	dir := filepath.Dir(basePath)
	base := filepath.Base(basePath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	label := fmt.Sprintf("%s.part%04d", name, part)
	if ext != "" {
		label += ext
	}
	return filepath.Join(dir, label)
}

func failSplit(summary SplitSummary, err error) (SplitSummary, error) {
	if cleanupErr := cleanupSplitOutputs(summary.Outputs); cleanupErr != nil {
		return summary, errors.Join(err, cleanupErr)
	}
	return summary, err
}

func cleanupSplitOutputs(paths []string) error {
	var cleanupErr error
	for _, path := range paths {
		if err := removeFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove split output %q: %w", path, err))
		}
	}
	return cleanupErr
}
