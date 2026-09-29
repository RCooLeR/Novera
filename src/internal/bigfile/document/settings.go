package document

import "sync"

var documentSettings = struct {
	mu                sync.RWMutex
	cacheMaxBytes     int
	persistIndexCache bool
}{
	cacheMaxBytes:     defaultChunkCacheMaxBytes,
	persistIndexCache: true,
}

func SetCacheMaxBytes(maxBytes int) {
	documentSettings.mu.Lock()
	if maxBytes <= 0 {
		maxBytes = defaultChunkCacheMaxBytes
	}
	documentSettings.cacheMaxBytes = maxBytes
	documentSettings.mu.Unlock()
}

func currentCacheMaxBytes() int {
	documentSettings.mu.RLock()
	defer documentSettings.mu.RUnlock()
	return documentSettings.cacheMaxBytes
}

func SetPersistentIndexCache(enabled bool) {
	documentSettings.mu.Lock()
	documentSettings.persistIndexCache = enabled
	documentSettings.mu.Unlock()
}

func persistentIndexCacheEnabled() bool {
	documentSettings.mu.RLock()
	defer documentSettings.mu.RUnlock()
	return documentSettings.persistIndexCache
}

func (d *FileDocument) SetCacheMaxBytes(maxBytes int) {
	if d == nil || d.cache == nil {
		return
	}
	d.cache.setMaxBytes(maxBytes)
}
