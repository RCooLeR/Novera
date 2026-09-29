package document

import "testing"

func TestChunkCacheReusesLoadedChunks(t *testing.T) {
	cache := newChunkCache(4, 16)
	data := []byte("abcdefghijkl")
	loads := 0

	load := func(start int64, size int) ([]byte, error) {
		loads++
		end := start + int64(size)
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		return data[start:end], nil
	}

	first, err := cache.readRange(0, 5, load)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "abcde" {
		t.Fatalf("first = %q, want abcde", string(first))
	}

	second, err := cache.readRange(2, 7, load)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != "cdefg" {
		t.Fatalf("second = %q, want cdefg", string(second))
	}
	if loads != 2 {
		t.Fatalf("loads = %d, want 2", loads)
	}
}

func TestChunkCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newChunkCache(4, 8)
	data := []byte("abcdefghijkl")
	loads := 0

	load := func(start int64, size int) ([]byte, error) {
		loads++
		end := start + int64(size)
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		return data[start:end], nil
	}

	if _, err := cache.readRange(0, 4, load); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.readRange(4, 8, load); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.readRange(8, 12, load); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.readRange(0, 4, load); err != nil {
		t.Fatal(err)
	}
	if loads != 4 {
		t.Fatalf("loads = %d, want 4 after LRU eviction", loads)
	}
}
