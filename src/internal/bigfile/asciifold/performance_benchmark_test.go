package asciifold

import (
	"bytes"
	"testing"
)

func BenchmarkFoldedRepeatedPrefix(b *testing.B) {
	haystack := bytes.Repeat([]byte("A"), 256<<10)
	needle := bytes.Repeat([]byte("a"), 1024)
	needle[len(needle)-2] = 'b'
	for _, direction := range []string{"forward", "backward"} {
		b.Run(direction, func(b *testing.B) {
			find := IndexFolded
			if direction == "backward" {
				find = LastIndexFolded
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(haystack)))
			for b.Loop() {
				if got := find(haystack, needle); got != -1 {
					b.Fatalf("offset=%d, want -1", got)
				}
			}
		})
	}
}
