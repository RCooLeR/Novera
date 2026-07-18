package lineindex

import (
	"bytes"
	"context"
	"testing"
)

func BenchmarkBuildShortLines16MiB(b *testing.B) {
	line := bytes.Repeat([]byte("x"), 79)
	line = append(line, '\n')
	body := bytes.Repeat(line, (16<<20)/len(line))

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for range b.N {
		idx := New(4096)
		if err := idx.Build(context.Background(), bytes.NewReader(body)); err != nil {
			b.Fatal(err)
		}
		if !idx.Done() {
			b.Fatal("index did not complete")
		}
	}
}

func BenchmarkBuildGiantLine16MiB(b *testing.B) {
	body := bytes.Repeat([]byte("x"), 16<<20)
	body = append(body, '\n')

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for range b.N {
		idx := New(4096)
		if err := idx.Build(context.Background(), bytes.NewReader(body)); err != nil {
			b.Fatal(err)
		}
		if !idx.Done() {
			b.Fatal("index did not complete")
		}
	}
}
