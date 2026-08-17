package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAllLegacyFileTransformsRejectSwapBeforeValidationAndFilesystemAccess(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, string, string) (FileSummary, error)
	}{
		{
			name: "plain replace",
			run: func(ctx context.Context, sourcePath string, outputPath string) (FileSummary, error) {
				return ReplacePlainFile(ctx, sourcePath, outputPath, nil, nil, FileOptions{SwapOriginal: true})
			},
		},
		{
			name: "regexp replace",
			run: func(ctx context.Context, sourcePath string, outputPath string) (FileSummary, error) {
				return ReplaceRegexpFile(ctx, sourcePath, outputPath, []byte("("), nil, FileOptions{SwapOriginal: true}, RegexOptions{})
			},
		},
		{
			name: "batch plain replace",
			run: func(ctx context.Context, sourcePath string, outputPath string) (FileSummary, error) {
				return ReplaceBatchPlainFile(ctx, sourcePath, outputPath, nil, FileOptions{SwapOriginal: true}, BatchOptions{})
			},
		},
		{
			name: "batch regexp replace",
			run: func(ctx context.Context, sourcePath string, outputPath string) (FileSummary, error) {
				return ReplaceBatchRegexpFile(ctx, sourcePath, outputPath, nil, FileOptions{SwapOriginal: true}, RegexOptions{})
			},
		},
		{
			name: "encoding conversion",
			run: func(ctx context.Context, sourcePath string, outputPath string) (FileSummary, error) {
				return ConvertEncodingFile(ctx, sourcePath, outputPath, "not-an-encoding", FileOptions{SwapOriginal: true})
			},
		},
		{
			name: "line-ending conversion",
			run: func(ctx context.Context, sourcePath string, outputPath string) (FileSummary, error) {
				return ConvertLineEndingsFile(ctx, sourcePath, outputPath, "not-a-line-ending", FileOptions{SwapOriginal: true})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "must-not-be-created")
			summary, err := tt.run(context.Background(), filepath.Join(dir, "source.txt"), filepath.Join(dir, "output.txt"))
			if !errors.Is(err, ErrSwapOriginalDisabled) {
				t.Fatalf("error = %v, want ErrSwapOriginalDisabled", err)
			}
			if summary != (FileSummary{}) {
				t.Fatalf("summary = %+v, want zero summary", summary)
			}
			if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("disabled transform created filesystem artifacts: %v", statErr)
			}
		})
	}
}
