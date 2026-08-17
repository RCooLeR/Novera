package exportx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"novera/internal/bigfile/fileio"
)

const defaultExportManifestSuffix = ".quarry-export-manifest.json"

func shouldWriteExportManifest(opts Options) bool {
	return opts.WriteManifest || strings.TrimSpace(opts.ManifestPath) != ""
}

func exportManifestPathFor(outputPath string, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	return outputPath + defaultExportManifestSuffix
}

func ensureExportManifestAvailable(path string, protectedPaths ...string) error {
	return ensureManifestAvailable("export", path, protectedPaths...)
}

func ensureManifestAvailable(kind string, path string, protectedPaths ...string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New(kind + " manifest path is required")
	}
	if err := fileio.ValidateExactOutputPath(path); err != nil {
		return err
	}
	for _, protectedPath := range protectedPaths {
		same, err := fileio.SamePath(path, protectedPath)
		if err != nil {
			return err
		}
		if same {
			return fmt.Errorf("%w: %s manifest %s", fileio.ErrSourceAlias, kind, path)
		}
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w: %s", fileio.ErrExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tempPath := path + ".quarry.tmp"
	if _, err := fileio.Stat(tempPath); err == nil {
		return fmt.Errorf("%w: %s", fileio.ErrTempExists, tempPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeExportManifest(ctx context.Context, summary Summary) (retErr error) {
	if strings.TrimSpace(summary.ManifestPath) == "" {
		return errors.New("export manifest path is required")
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	manifest, err := fileio.OpenAtomicOutput(
		summary.ManifestPath,
		[]string{summary.SourcePath, summary.OutputPath},
		0o600,
	)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, manifest.Cleanup()) }()
	if n, err := manifest.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	return manifest.CommitContext(ctx)
}

func exportManifestPublicationError(summary Summary, err error) error {
	return &fileio.PublicationError{
		FinalPath: summary.OutputPath,
		Durable:   true,
		Err:       fmt.Errorf("export output was published, but manifest %q failed: %w", summary.ManifestPath, err),
	}
}
