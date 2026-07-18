package search

import (
	"bytes"
	"context"
	"testing"
)

func BenchmarkFindPlain32MiB(b *testing.B) {
	body := bytes.Repeat([]byte("a"), 32<<20)
	needle := []byte("Novera-needle")
	copy(body[len(body)-len(needle)-1:], needle)
	reader := memReaderAt{data: body}

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for range b.N {
		matches := 0
		err := FindPlain(context.Background(), reader, needle, PlainOptions{ChunkSize: 1 << 20}, func(Match) error {
			matches++
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
		if matches != 1 {
			b.Fatalf("matches = %d, want 1", matches)
		}
	}
}
