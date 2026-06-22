package exportx

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"novera/internal/bigfile/fileio"
)

const defaultExportManifestSuffix = ".quarry-export-manifest.json"

func shouldWriteExportManifest(opts Options) bool {
	return opts.WriteManifest || strings.TrimSpace(opts.ManifestPath) != ""
}

func exportManifestPathFor(outputPath string, override string) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	return outputPath + defaultExportManifestSuffix
}

func ensureExportManifestAvailable(path string) error {
	return ensureManifestAvailable("export", path)
}

func ensureManifestAvailable(kind string, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New(kind + " manifest path is required")
	}
	if _, err := fileio.Stat(path); err == nil {
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

func writeExportManifest(summary Summary) error {
	if strings.TrimSpace(summary.ManifestPath) == "" {
		return errors.New("export manifest path is required")
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = fileio.WriteFileAtomic(summary.ManifestPath, data, fileio.AtomicWriteOptions{Mode: 0o600})
	return err
}

func cleanupOutputAfterManifestFailure(outputPath string, err error) error {
	if cleanupErr := removeFile(outputPath); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
		return errors.Join(err, fmt.Errorf("remove export output %q: %w", outputPath, cleanupErr))
	}
	return err
}
