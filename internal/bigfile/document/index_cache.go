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
)

const indexCacheVersion = 1
const indexCacheTailSampleSize = 64 * 1024

type indexCacheFile struct {
	Version         int                `json:"version"`
	Path            string             `json:"path"`
	Size            int64              `json:"size"`
	ModTimeUnixNano int64              `json:"modTimeUnixNano"`
	SampleHash      string             `json:"sampleHash"`
	Index           lineindex.Snapshot `json:"index"`
}

func indexCachePath(path string) string {
	central, err := cachepath.SourcePath("indexes", path, ".quarry-index.json")
	if err == nil {
		return central
	}
	return legacyIndexCachePath(path)
}

func legacyIndexCachePath(path string) string {
	return path + ".quarry-index.json"
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
	for _, candidate := range indexCacheReadPaths(path) {
		data, err := os.ReadFile(candidate)
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
		return lineindex.FromSnapshot(cache.Index), true
	}
	return nil, false
}

func indexCacheReadPaths(path string) []string {
	central := indexCachePath(path)
	legacy := legacyIndexCachePath(path)
	if central == legacy {
		return []string{legacy}
	}
	return []string{central, legacy}
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
