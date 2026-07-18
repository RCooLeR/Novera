package document

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
const indexCacheTailSampleSize = 64 * 1024
const maxIndexCacheFileSize = 128 * 1024 * 1024

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
	hash := sha256.New()
	_, _ = hash.Write(head)
	if size > int64(len(head)) {
		start := size - indexCacheTailSampleSize
		if start < int64(len(head)) {
			start = int64(len(head))
		}
		if start < size {
			tail := make([]byte, size-start)
			n, err := f.ReadAt(tail, start)
			if err == nil || errors.Is(err, io.EOF) {
				_, _ = hash.Write(tail[:n])
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// loadIndexCache treats stable size, mtime, and bounded head/tail hashes as a
// cache-validity signal, not a tamper-proof integrity proof. Deliberate
// middle-of-file edits that preserve those signals can evade the cache check.
func loadIndexCache(path string, size int64, modTime time.Time, sampleHash string) (*lineindex.Index, bool) {
	if !persistentIndexCacheEnabled() {
		return nil, false
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
			!cache.Index.Done {
			continue
		}
		if candidate != canonical {
			migrateValidatedIndexCache(canonical, data)
		}
		return lineindex.FromSnapshot(cache.Index), true
	}
	return nil, false
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
