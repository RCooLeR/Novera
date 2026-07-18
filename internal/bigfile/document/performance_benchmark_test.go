package document

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkOpenSparse1GiB(b *testing.B) {
	path := filepath.Join(b.TempDir(), "sparse-1gib.txt")
	file, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := file.WriteString("Novera benchmark fixture\n"); err != nil {
		b.Fatal(err)
	}
	if err := file.Truncate(1 << 30); err != nil {
		b.Fatal(err)
	}
	if err := file.Close(); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		doc, err := OpenFile(path)
		if err != nil {
			b.Fatal(err)
		}
		if err := doc.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVisiblePageShortLines(b *testing.B) {
	line := bytes.Repeat([]byte("x"), 79)
	line = append(line, '\n')
	body := bytes.Repeat(line, (16<<20)/len(line))
	path := filepath.Join(b.TempDir(), "short-lines.txt")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		b.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = doc.Close() })

	opts := VisibleLineOptions{
		MaxBytes:           256 << 10,
		MaxLineBytes:       64 << 10,
		LongLineLimitBytes: 64 << 10,
	}
	b.ReportAllocs()
	b.SetBytes(int64(opts.MaxBytes))
	b.ResetTimer()
	for range b.N {
		page, err := doc.VisiblePageFromOffset(int64(len(body)/2), 200, opts)
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Lines) == 0 {
			b.Fatal("visible page unexpectedly empty")
		}
	}
}

func BenchmarkVisiblePageGiantLine(b *testing.B) {
	body := bytes.Repeat([]byte("x"), 16<<20)
	body = append(body, '\n')
	path := filepath.Join(b.TempDir(), "giant-line.txt")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		b.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = doc.Close() })

	opts := VisibleLineOptions{
		MaxBytes:             256 << 10,
		MaxLineBytes:         64 << 10,
		LongLineLimitBytes:   64 << 10,
		HorizontalByteOffset: 8 << 20,
	}
	b.ReportAllocs()
	b.SetBytes(int64(opts.MaxBytes))
	b.ResetTimer()
	for range b.N {
		page, err := doc.VisiblePageFromOffset(0, 1, opts)
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Lines) != 1 || !page.Lines[0].ExceedsRenderLimit {
			b.Fatal("giant line was not returned as one bounded, truncated visual line")
		}
	}
}
