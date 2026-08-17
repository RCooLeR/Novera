package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

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
	// PrepareSourceValidation runs once after every destination has passed
	// exact-path, collision, and source-alias preflight, but before source bytes
	// are consumed. The returned validator runs immediately before the success
	// manifest is published. This lets a caller capture and later prove one
	// exact source generation without a full-file rehash for every table.
	PrepareSourceValidation func(context.Context) (func(context.Context) error, error)
	// ValidateSource is an optional operation-level source-generation check.
	// It runs immediately before each table output and the completion manifest
	// are published. A failure leaves only outputs whose own publication was
	// already validated and completed.
	ValidateSource     func(context.Context) error
	ValidateCompletion func(context.Context) error
}

type WriteSummary struct {
	Operation            string
	SourcePath           string
	SourceSize           int64
	ManifestPath         string
	Outputs              []TableRange
	BytesWritten         int64
	Complete             bool
	Failure              string   `json:",omitempty"`
	ChecksumAlgorithm    string   `json:",omitempty"`
	HeaderIncluded       bool     // false: slices omit the dump preamble (see Note)
	DetectedCharsets     []string `json:",omitempty"`
	Note                 string   `json:",omitempty"`
	PublicationUncertain bool     `json:",omitempty"`
}

// IncompleteWriteError reports a multi-output extraction that stopped after
// one or more complete files were safely published. Those files are retained;
// Quarry never removes a mutable pathname merely because it was present in an
// earlier in-memory summary.
type IncompleteWriteError struct {
	Summary WriteSummary
	Err     error
}

func (e *IncompleteWriteError) Error() string {
	return fmt.Sprintf("SQL extraction incomplete after %d committed output(s): %v", len(e.Summary.Outputs), e.Err)
}

func (e *IncompleteWriteError) Unwrap() error { return e.Err }

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
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.OutputDir == "" {
		return WriteSummary{}, errors.New("output directory is required")
	}
	if err := fileio.ValidateExactDirectoryPath(opts.OutputDir); err != nil {
		return WriteSummary{}, err
	}
	if len(preview.Tables) == 0 {
		return WriteSummary{}, errors.New("no SQL table ranges to write")
	}
	manifestPath := opts.ManifestPath
	if manifestPath == "" {
		var err error
		manifestPath, err = fileio.ExactChildPath(opts.OutputDir, defaultManifestName)
		if err != nil {
			return WriteSummary{}, err
		}
	}
	if err := requireDirectChild(opts.OutputDir, manifestPath); err != nil {
		return WriteSummary{}, fmt.Errorf("unsafe manifest path: %w", err)
	}
	if err := os.MkdirAll(opts.OutputDir, 0o700); err != nil {
		return WriteSummary{}, err
	}
	if err := preflightWritePaths(sourcePath, manifestPath, preview.Tables); err != nil {
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
	sourceDoc := doc
	completionValidation := opts.ValidateSource
	if opts.PrepareSourceValidation != nil {
		prepared, err := opts.PrepareSourceValidation(ctx)
		if err != nil {
			return failWrite(summary, err)
		}
		if prepared == nil {
			return failWrite(summary, errors.New("prepared SQL extraction source validator is nil"))
		}
		completionValidation = composeSourceValidators(opts.ValidateSource, prepared)
	}
	completionValidation = composeSourceValidators(completionValidation, opts.ValidateCompletion)
	done := int64(0)
	for i, table := range preview.Tables {
		if table.OutputPath == "" {
			return failWrite(summary, fmt.Errorf("table %q has no output path", table.Name))
		}
		if err := fileio.ValidateExactOutputPath(table.OutputPath); err != nil {
			return failWrite(summary, fmt.Errorf("unsafe output path for table %q: %w", table.Name, err))
		}
		baseDone := done
		part, err := exportx.ExportByteRanges(ctx, sourceDoc, sourcePath, table.OutputPath, ByteRanges(table), exportx.Options{
			ComputeSHA256: opts.ComputeSHA256,
			ValidateSource: func(validateCtx context.Context) error {
				return validateWriteSource(validateCtx, doc, opts.ValidateSource)
			},
			Progress: func(written int64, partTotal int64) {
				if opts.Progress != nil {
					opts.Progress(baseDone+written, total, i+1)
				}
			},
		})
		if err != nil {
			var publication *fileio.PublicationError
			if errors.As(err, &publication) && publication.LocationUncertain {
				summary.PublicationUncertain = true
			} else if errors.As(err, &publication) && publication.FinalPath == table.OutputPath {
				table.Bytes = part.BytesWritten
				table.SHA256 = part.SHA256
				summary.Outputs = append(summary.Outputs, table)
				done += part.BytesWritten
				summary.BytesWritten += part.BytesWritten
			}
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
	summary.Complete = true
	if err := writeManifest(ctx, doc, completionValidation, summary); err != nil {
		return finishWrite(summary, err)
	}
	return summary, nil
}

// finishWrite classifies errors from publication of the completion manifest.
// A non-uncertain PublicationError for the requested manifest path means the
// complete manifest is known to be visible, even though its durability or a
// later finalization step failed. In that case the extraction is complete and
// callers receive the original publication warning. An uncertain publication
// cannot support a success claim and is reported as an incomplete operation.
func finishWrite(summary WriteSummary, err error) (WriteSummary, error) {
	if err == nil {
		return summary, nil
	}
	var publication *fileio.PublicationError
	if errors.As(err, &publication) {
		if publication.LocationUncertain {
			summary.PublicationUncertain = true
		} else if publication.FinalPath == summary.ManifestPath {
			summary.Complete = true
			summary.Failure = ""
			return summary, err
		}
	}
	return failWrite(summary, err)
}

func composeSourceValidators(first, last func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		if first != nil {
			if err := first(ctx); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if last != nil {
			return last(ctx)
		}
		return ctx.Err()
	}
}

type retainedSourceValidator interface {
	ValidateUnchanged() error
}

func validateWriteSource(ctx context.Context, doc document.ReaderAtSize, validate func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if validate != nil {
		if err := validate(ctx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if retained, ok := doc.(retainedSourceValidator); ok {
		if err := retained.ValidateUnchanged(); err != nil {
			return fmt.Errorf("SQL extraction source generation changed: %w", err)
		}
	}
	return ctx.Err()
}

func writeManifest(ctx context.Context, doc document.ReaderAtSize, validate func(context.Context) error, summary WriteSummary) (retErr error) {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	sources := make([]string, 0, len(summary.Outputs)+1)
	sources = append(sources, summary.SourcePath)
	for _, output := range summary.Outputs {
		sources = append(sources, output.OutputPath)
	}
	out, err := fileio.OpenAtomicOutput(summary.ManifestPath, sources, 0o600)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, out.Cleanup()) }()
	written, err := out.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return errors.New("short SQL extraction manifest write")
	}
	return out.CommitContextValidated(ctx, func(ctx context.Context) error {
		return validateWriteSource(ctx, doc, validate)
	})
}

func preflightWritePaths(sourcePath string, manifestPath string, tables []TableRange) error {
	paths := make([]string, 0, len(tables)+1)
	paths = append(paths, manifestPath)
	for _, table := range tables {
		if table.OutputPath == "" {
			return fmt.Errorf("table %q has no output path", table.Name)
		}
		if err := fileio.ValidateExactOutputPath(table.OutputPath); err != nil {
			return fmt.Errorf("unsafe output path for table %q: %w", table.Name, err)
		}
		paths = append(paths, table.OutputPath)
	}
	for i, path := range paths {
		sameSource, err := fileio.SamePath(sourcePath, path)
		if err != nil {
			return err
		}
		if sameSource {
			return fmt.Errorf("%w: %s", fileio.ErrSourceAlias, path)
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%w: %s", fileio.ErrExists, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, prior := range paths[:i] {
			same, err := fileio.SamePath(prior, path)
			if err != nil {
				return err
			}
			if same {
				return fmt.Errorf("planned SQL extraction paths alias: %q and %q", prior, path)
			}
		}
	}
	return nil
}

func failWrite(summary WriteSummary, err error) (WriteSummary, error) {
	summary.Complete = false
	if err != nil {
		summary.Failure = err.Error()
	}
	if len(summary.Outputs) > 0 {
		return summary, &IncompleteWriteError{Summary: summary, Err: err}
	}
	return summary, err
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
