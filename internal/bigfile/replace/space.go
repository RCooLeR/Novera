package replace

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"

	"novera/internal/bigfile/regularfile"
	"novera/internal/bigfile/search"
)

const defaultSpaceSafetyBytes int64 = 64 * 1024 * 1024

// SpaceOptions controls output-size and disk-space estimates.
type SpaceOptions struct {
	ChunkSize       int
	SafetyBytes     int64
	CaseInsensitive bool
	WholeWord       bool
}

// SpaceEstimate describes the expected output size and available target space.
type SpaceEstimate struct {
	SourceSize     int64
	OutputSize     int64
	RequiredBytes  int64
	AvailableBytes uint64
	SafetyBytes    int64
	Matches        int64
	CountedMatches bool
	OK             bool
}

// EstimatePlainOutputSize returns the exact output size for a plain replace.
func EstimatePlainOutputSize(ctx context.Context, r ReaderAtSize, pattern []byte, repl []byte, opts SpaceOptions) (SpaceEstimate, error) {
	if err := validatePlainTransformInputs(pattern, repl, opts.ChunkSize); err != nil {
		return SpaceEstimate{}, err
	}
	if r.Size() < 0 {
		return SpaceEstimate{}, errors.New("source size must not be negative")
	}

	estimate := SpaceEstimate{
		SourceSize: r.Size(),
		OutputSize: r.Size(),
	}
	delta := int64(len(repl) - len(pattern))
	if delta == 0 {
		return estimate, nil
	}

	err := search.FindPlain(ctx, r, pattern, search.PlainOptions{
		ChunkSize:       opts.ChunkSize,
		CaseInsensitive: opts.CaseInsensitive,
		WholeWord:       opts.WholeWord,
	}, func(search.Match) error {
		estimate.Matches++
		return nil
	})
	if err != nil {
		return SpaceEstimate{}, err
	}

	if delta > 0 && estimate.Matches > (math.MaxInt64-estimate.SourceSize)/delta {
		return SpaceEstimate{}, errors.New("estimated output size overflows int64")
	}
	estimate.OutputSize = estimate.SourceSize + estimate.Matches*delta
	if estimate.OutputSize < 0 {
		return SpaceEstimate{}, errors.New("estimated output size is negative")
	}
	estimate.CountedMatches = true
	return estimate, nil
}

// CheckPlainReplaceSpace estimates output size and compares it to free space in the output directory.
func CheckPlainReplaceSpace(ctx context.Context, sourcePath string, outputPath string, pattern []byte, repl []byte, opts SpaceOptions) (SpaceEstimate, error) {
	src, err := regularfile.Open(sourcePath)
	if err != nil {
		return SpaceEstimate{}, err
	}
	defer src.Close()

	st, err := src.Stat()
	if err != nil {
		return SpaceEstimate{}, err
	}

	reader := fileReaderAtSize{file: src, size: st.Size()}
	estimate, err := EstimatePlainOutputSize(ctx, reader, pattern, repl, opts)
	if err != nil {
		return SpaceEstimate{}, err
	}

	outputDir := filepath.Dir(outputPath)
	available, err := availableDiskBytes(outputDir)
	if err != nil {
		return SpaceEstimate{}, err
	}

	safetyBytes := opts.SafetyBytes
	if safetyBytes <= 0 {
		safetyBytes = defaultSpaceSafetyBytes
	}
	if estimate.OutputSize > math.MaxInt64-safetyBytes {
		return SpaceEstimate{}, errors.New("required disk space overflows int64")
	}

	estimate.SafetyBytes = safetyBytes
	estimate.RequiredBytes = estimate.OutputSize + safetyBytes
	estimate.AvailableBytes = available
	estimate.OK = uint64(estimate.RequiredBytes) <= available
	return estimate, nil
}

type fileReaderAtSize struct {
	file *os.File
	size int64
}

func (f fileReaderAtSize) ReadAt(p []byte, off int64) (int, error) {
	return f.file.ReadAt(p, off)
}

func (f fileReaderAtSize) Size() int64 {
	return f.size
}
