package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"novera/internal/bigfile/cachepath"
	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/lineindex"
	"novera/internal/persistfile"
)

const indexCacheVersion = 1
const maxIndexCacheFileSize = 32 * 1024 * 1024
const indexCacheFingerprintChunkSize = 1024 * 1024

type indexCacheFile struct {
	Version         int                `json:"version"`
	Path            string             `json:"path"`
	Size            int64              `json:"size"`
	ModTimeUnixNano int64              `json:"modTimeUnixNano"`
	SampleHash      string             `json:"sampleHash"`
	Index           lineindex.Snapshot `json:"index"`
}

func indexCachePath(path string) string {
	central, err := cachepath.SourcePath("indexes", path, ".novera-index.json")
	if err == nil {
		return central
	}
	return fallbackIndexCachePath(path)
}

func fallbackIndexCachePath(path string) string {
	return path + ".novera-index.json"
}

// legacyIndexCachePath is retained as read-only compatibility with recovery
// sidecars written beside source files by the pre-Novera large-file engine.
func legacyIndexCachePath(path string) string {
	return path + ".quarry-index.json"
}

func legacyCentralIndexCachePath(path string) (string, bool) {
	legacy, available, err := cachepath.LegacySourcePath("indexes", path, ".quarry-index.json")
	if err != nil || !available {
		return "", false
	}
	return legacy, true
}

func computeIndexSampleHash(f *os.File, size int64, head []byte) string {
	_ = head
	digest, _ := computeIndexSourceHashContext(context.Background(), f, size)
	return digest
}

// computeIndexSourceHashContext fingerprints the complete retained source.
// Size/mtime plus head/tail samples are not sufficient for exact sparse
// anchors: a middle-only rewrite with restored metadata must invalidate them.
func computeIndexSourceHashContext(ctx context.Context, f *os.File, size int64) (string, error) {
	if f == nil {
		return "", errors.New("index-cache source is required")
	}
	if size < 0 {
		return "", errors.New("index-cache source size is negative")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	hash := sha256.New()
	buffer := make([]byte, indexCacheFingerprintChunkSize)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		want := int64(len(buffer))
		if remaining := size - offset; want > remaining {
			want = remaining
		}
		n, readErr := f.ReadAt(buffer[:int(want)], offset)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
			offset += int64(n)
		}
		if n != int(want) {
			if readErr == nil {
				readErr = io.ErrUnexpectedEOF
			}
			return "", fmt.Errorf("index-cache fingerprint read %d of %d bytes: %w", n, want, readErr)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// loadIndexCacheSnapshot requires a full source digest in addition to stable
// metadata and returns an owned, validated snapshot for in-place restoration.
func loadIndexCacheSnapshot(path string, size int64, modTime time.Time, sampleHash string, expectedStride int64) (lineindex.Snapshot, bool) {
	if !persistentIndexCacheEnabled() {
		return lineindex.Snapshot{}, false
	}
	canonical := indexCachePath(path)
	for _, candidate := range indexCacheReadPaths(path) {
		data, err := persistfile.Read(candidate, maxIndexCacheFileSize)
		if err != nil {
			continue
		}
		var cache indexCacheFile
		if err := json.Unmarshal(data, &cache); err != nil {
			continue
		}
		if cache.Version != indexCacheVersion ||
			cache.Path != path ||
			cache.Size != size ||
			cache.ModTimeUnixNano != modTime.UnixNano() ||
			cache.SampleHash != sampleHash ||
			cache.Index.EveryLines != expectedStride ||
			!cache.Index.Done {
			continue
		}
		if err := lineindex.ValidateSnapshot(cache.Index, size); err != nil {
			continue
		}
		if candidate != canonical {
			migrateValidatedIndexCache(canonical, data)
		}
		return cache.Index, true
	}
	return lineindex.Snapshot{}, false
}

func indexCacheReadPaths(path string) []string {
	central := indexCachePath(path)
	candidates := []string{central}
	if legacyCentral, ok := legacyCentralIndexCachePath(path); ok {
		candidates = appendUniquePath(candidates, legacyCentral)
	}
	candidates = appendUniquePath(candidates, legacyIndexCachePath(path))
	return candidates
}

func indexCacheCandidateExists(path string) bool {
	if !persistentIndexCacheEnabled() {
		return false
	}
	for _, candidate := range indexCacheReadPaths(path) {
		info, err := os.Lstat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func appendUniquePath(paths []string, candidate string) []string {
	for _, existing := range paths {
		if filepath.Clean(existing) == filepath.Clean(candidate) {
			return paths
		}
	}
	return append(paths, candidate)
}

func migrateValidatedIndexCache(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_, _ = fileio.WriteFileAtomic(path, data, fileio.AtomicWriteOptions{
		Mode:       0o600,
		Overwrite:  false,
		TempSuffix: ".migration.tmp",
	})
}

func saveIndexCache(path string, size int64, modTime time.Time, sampleHash string, idx *lineindex.Index) error {
	if !persistentIndexCacheEnabled() {
		return nil
	}
	snapshot := idx.Snapshot()
	if !snapshot.Done {
		return nil
	}
	data, err := json.MarshalIndent(indexCacheFile{
		Version:         indexCacheVersion,
		Path:            path,
		Size:            size,
		ModTimeUnixNano: modTime.UnixNano(),
		SampleHash:      sampleHash,
		Index:           snapshot,
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	cachePath := indexCachePath(path)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return err
	}
	_, err = fileio.WriteFileAtomic(cachePath, data, fileio.AtomicWriteOptions{
		Mode:       0o600,
		Overwrite:  true,
		TempSuffix: ".tmp",
	})
	return err
}
