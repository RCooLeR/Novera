package replace

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// InPlaceOptions controls guarded same-length patching of the source file.
type InPlaceOptions struct {
	ChunkSize       int
	CaseInsensitive bool
	WholeWord       bool
	Progress        func(Progress)
}

// InPlaceSummary describes the result of a guarded in-place patch.
type InPlaceSummary struct {
	SourcePath   string
	ManifestPath string
	Patched      bool
	Matches      int64
	BytesPatched int64
}

var ErrInPlacePatchDisabled = errors.New("in-place patching is disabled because it can partially mutate the source; use safe output replace instead")

// ReplacePlainFileInPlace used to patch sourcePath directly for same-length replacements.
// Quarry now refuses that public path because it can leave the original partially mutated
// after a crash or cancellation. Use the safe output/manifest replacement pipeline instead.
func ReplacePlainFileInPlace(ctx context.Context, sourcePath string, pattern []byte, repl []byte, opts InPlaceOptions) (InPlaceSummary, error) {
	_ = ctx
	_ = opts
	manifestPath := inplaceManifestPath(sourcePath)
	summary := InPlaceSummary{
		SourcePath:   sourcePath,
		ManifestPath: manifestPath,
	}
	if len(pattern) == 0 {
		return summary, errors.New("empty pattern")
	}
	if len(pattern) != len(repl) {
		return summary, errors.New("in-place patch requires the replacement to be the same length as the pattern")
	}
	return summary, ErrInPlacePatchDisabled
}

func inplaceManifestPath(sourcePath string) string {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	return filepath.Clean(sourcePath) + ".quarry.inplace." + stamp + ".manifest.json"
}
