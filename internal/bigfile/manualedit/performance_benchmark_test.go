package manualedit

import (
	"bytes"
	"context"
	"io"
	"testing"
)

type benchmarkReaderAt struct{ data []byte }

func (r benchmarkReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r benchmarkReaderAt) Size() int64 { return int64(len(r.data)) }

type benchmarkDiscardWriter struct{ bytes int64 }

func (w *benchmarkDiscardWriter) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	return len(p), nil
}

func (*benchmarkDiscardWriter) Sync() error { return nil }

func BenchmarkStage128Edits(b *testing.B) {
	text := bytes.Repeat([]byte("x"), 64)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		session := NewSession(256<<20, DefaultMaxInsertedBytes)
		for range 128 {
			at := session.Size()
			if err := session.ApplyEdit(Edit{Start: at, End: at, Text: text}); err != nil {
				b.Fatal(err)
			}
		}
		if session.EditCount() != 128 {
			b.Fatalf("edit count = %d, want 128", session.EditCount())
		}
	}
}

func BenchmarkRenderStagedSave16MiB(b *testing.B) {
	source := bytes.Repeat([]byte("0123456789abcdef"), 1<<20)
	session := NewSession(int64(len(source)), DefaultMaxInsertedBytes)
	for i := 0; i < 64; i++ {
		at := int64(i) * int64(len(source)/64)
		if err := session.ApplyEdit(Edit{Start: at, End: at, Text: []byte("EDIT")}); err != nil {
			b.Fatal(err)
		}
	}
	reader := benchmarkReaderAt{data: source}
	writer := &benchmarkDiscardWriter{}

	b.ReportAllocs()
	b.SetBytes(session.Size())
	b.ResetTimer()
	for range b.N {
		writer.bytes = 0
		written, err := session.table.WriteTo(context.Background(), reader, writer, WriteOptions{})
		if err != nil {
			b.Fatal(err)
		}
		if written != session.Size() || writer.bytes != written {
			b.Fatalf("written = %d, sink = %d, want %d", written, writer.bytes, session.Size())
		}
	}
}
