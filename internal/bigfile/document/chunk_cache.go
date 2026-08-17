package document

import (
	"container/list"
	"errors"
	"sync"
)

const (
	defaultChunkCacheChunkSize = 64 * 1024
	defaultChunkCacheMaxBytes  = 8 * 1024 * 1024
)

type chunkLoader func(start int64, size int) ([]byte, error)

type chunkCache struct {
	mu        sync.Mutex
	chunkSize int
	maxBytes  int
	bytes     int
	order     *list.List
	chunks    map[int64]*cachedChunk
}

type cachedChunk struct {
	start int64
	data  []byte
	elem  *list.Element
}

func newChunkCache(chunkSize int, maxBytes int) *chunkCache {
	if chunkSize <= 0 {
		chunkSize = defaultChunkCacheChunkSize
	}
	if maxBytes < chunkSize {
		maxBytes = chunkSize
	}
	return &chunkCache{
		chunkSize: chunkSize,
		maxBytes:  maxBytes,
		order:     list.New(),
		chunks:    make(map[int64]*cachedChunk),
	}
}

func (c *chunkCache) readRange(start int64, end int64, load chunkLoader) ([]byte, error) {
	if c == nil {
		return nil, errors.New("nil chunk cache")
	}
	if end < start {
		end = start
	}
	out := make([]byte, 0, end-start)
	for pos := start; pos < end; {
		chunkStart := c.align(pos)
		chunk, err := c.getOrLoad(chunkStart, load)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			break
		}

		from := int(pos - chunkStart)
		if from >= len(chunk) {
			break
		}
		to := len(chunk)
		if want := int(end - chunkStart); want < to {
			to = want
		}
		if to < from {
			to = from
		}
		out = append(out, chunk[from:to]...)
		pos = chunkStart + int64(to)
	}
	return out, nil
}

func (c *chunkCache) align(offset int64) int64 {
	size := int64(c.chunkSize)
	return offset / size * size
}

func (c *chunkCache) getOrLoad(start int64, load chunkLoader) ([]byte, error) {
	c.mu.Lock()
	if chunk, ok := c.chunks[start]; ok {
		c.order.MoveToFront(chunk.elem)
		data := chunk.data
		c.mu.Unlock()
		return data, nil
	}
	c.mu.Unlock()

	data, err := load(start, c.chunkSize)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if chunk, ok := c.chunks[start]; ok {
		c.order.MoveToFront(chunk.elem)
		return chunk.data, nil
	}

	stored := append([]byte(nil), data...)
	chunk := &cachedChunk{start: start, data: stored}
	chunk.elem = c.order.PushFront(chunk)
	c.chunks[start] = chunk
	c.bytes += len(stored)
	c.evict()
	return stored, nil
}

func (c *chunkCache) evict() {
	for c.bytes > c.maxBytes {
		elem := c.order.Back()
		if elem == nil {
			return
		}
		chunk := elem.Value.(*cachedChunk)
		c.order.Remove(elem)
		delete(c.chunks, chunk.start)
		c.bytes -= len(chunk.data)
	}
}

func (c *chunkCache) setMaxBytes(maxBytes int) {
	if c == nil {
		return
	}
	if maxBytes < c.chunkSize {
		maxBytes = c.chunkSize
	}
	c.mu.Lock()
	c.maxBytes = maxBytes
	c.evict()
	c.mu.Unlock()
}

func (c *chunkCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.bytes = 0
	c.order.Init()
	clear(c.chunks)
	c.mu.Unlock()
}
