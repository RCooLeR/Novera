package replace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/regexutil"
)

type shortRegexPreviewReader struct{ size int64 }

func (r shortRegexPreviewReader) Size() int64 { return r.size }
func (shortRegexPreviewReader) ReadAt([]byte, int64) (int, error) {
	return 0, io.EOF
}

type observedRegexWriter struct {
	bytes.Buffer
	writeCalls  int
	syncCalls   int
	bytesAtSync int
}

func (w *observedRegexWriter) Write(p []byte) (int, error) {
	w.writeCalls++
	return w.Buffer.Write(p)
}

func (w *observedRegexWriter) Sync() error {
	w.syncCalls++
	w.bytesAtSync = w.Len()
	return nil
}

type shortRegexWriter struct {
	writeCalls int
	syncCalls  int
}

func (w *shortRegexWriter) Write(p []byte) (int, error) {
	w.writeCalls++
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func (w *shortRegexWriter) Sync() error {
	w.syncCalls++
	return nil
}

func TestReplaceRegexpExactMaximumWidthAcrossEverySeam(t *testing.T) {
	const (
		chunkSize = 11
		match     = "a12345b"
	)
	pattern := []byte(`a[0-9]{5}b`)
	for bytesBeforeSeam := 1; bytesBeforeSeam < len(match); bytesBeforeSeam++ {
		t.Run(fmt.Sprintf("split-%d", bytesBeforeSeam), func(t *testing.T) {
			prefix := bytes.Repeat([]byte("."), chunkSize-bytesBeforeSeam)
			source := append(append(append([]byte(nil), prefix...), match...), " tail"...)
			got, matches, err := runReplaceRegexpForFuzz(t, source, pattern, []byte("X"), RegexOptions{
				ChunkSize:      chunkSize,
				MaxMatchWindow: len(match),
			})
			if err != nil {
				t.Fatal(err)
			}
			if matches != 1 || string(got) != string(prefix)+"X tail" {
				t.Fatalf("matches=%d output=%q", matches, got)
			}
		})
	}
}

func TestReplaceRegexpRejectsInexactPatternsBeforeWriting(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before a12345b after"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	output := &observedRegexWriter{}
	_, err = ReplaceRegexp(context.Background(), source, output, []byte(`a[0-9]{5}b`), []byte("X"), RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 6,
	})
	if !errors.Is(err, regexutil.ErrRegexExceedsWindow) {
		t.Fatalf("error = %v, want ErrRegexExceedsWindow", err)
	}
	if output.Len() != 0 || output.writeCalls != 0 || output.syncCalls != 0 {
		t.Fatalf("rejected replace touched output: %+v", output)
	}

	if _, err := source.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	_, err = ReplaceRegexp(context.Background(), source, output, []byte(`a.*b`), []byte("X"), RegexOptions{})
	if !errors.Is(err, regexutil.ErrUnboundedRegex) {
		t.Fatalf("error = %v, want ErrUnboundedRegex", err)
	}
}

func TestRegexFilePreflightDoesNotTouchFilesystem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-exist")
	_, err := ReplaceRegexpFile(
		context.Background(),
		filepath.Join(dir, "missing-source.txt"),
		filepath.Join(dir, "output.txt"),
		[]byte(`begin(?:.|\n)*end`),
		[]byte("X"),
		FileOptions{},
		RegexOptions{ChunkSize: 32, MaxMatchWindow: 16},
	)
	if !errors.Is(err, regexutil.ErrUnboundedRegex) {
		t.Fatalf("error = %v, want ErrUnboundedRegex", err)
	}
	if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("regex preflight touched output filesystem: %v", statErr)
	}
}

func TestRegexPreviewsRejectUnexpectedShortSource(t *testing.T) {
	r := shortRegexPreviewReader{size: 1024}
	if _, err := PreviewRegexp(context.Background(), r, []byte(`x`), []byte("y"), RegexPreviewOptions{MaxHits: 1}, RegexOptions{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("single preview error = %v, want io.ErrUnexpectedEOF", err)
	}
	if _, _, err := PreviewBatchRegexp(context.Background(), r, []BatchRule{{Find: []byte(`x`), Replace: []byte("y")}}, RegexPreviewOptions{MaxHits: 1}, RegexOptions{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("batch preview error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestDenseRegexOutputIsBufferedAndFlushedBeforeSync(t *testing.T) {
	source := bytes.Repeat([]byte("a"), 100_000)
	want := bytes.Repeat([]byte("xy"), len(source))
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch-%v", batch), func(t *testing.T) {
			writer := &observedRegexWriter{}
			matches, conflicts, err := runRegexWriterVariant(t, context.Background(), source, writer, batch, []byte("xy"), RegexOptions{
				ChunkSize:       4096,
				MaxMatchWindow:  1,
				WriteBufferSize: 1024,
			})
			if err != nil {
				t.Fatal(err)
			}
			if matches != int64(len(source)) || conflicts != 0 || !bytes.Equal(writer.Bytes(), want) {
				t.Fatalf("matches=%d conflicts=%d bytes=%d", matches, conflicts, writer.Len())
			}
			if writer.writeCalls >= len(source)/100 || writer.syncCalls != 1 || writer.bytesAtSync != len(want) {
				t.Fatalf("writes=%d syncs=%d bytesAtSync=%d", writer.writeCalls, writer.syncCalls, writer.bytesAtSync)
			}
		})
	}
}

func TestRegexOutputRejectsShortWritesWithoutSync(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch-%v", batch), func(t *testing.T) {
			writer := &shortRegexWriter{}
			_, _, err := runRegexWriterVariant(t, context.Background(), bytes.Repeat([]byte("a"), 64), writer, batch, []byte("x"), RegexOptions{
				ChunkSize:       64,
				MaxMatchWindow:  1,
				WriteBufferSize: 8,
			})
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("error = %v, want io.ErrShortWrite", err)
			}
			if writer.writeCalls == 0 || writer.syncCalls != 0 {
				t.Fatalf("writes=%d syncs=%d", writer.writeCalls, writer.syncCalls)
			}
		})
	}
}

func TestRegexWriteBufferBudgetIsValidatedBeforeOutput(t *testing.T) {
	for _, requested := range []int{-1, MaxRegexWriteBufferBytes + 1} {
		writer := &observedRegexWriter{}
		_, _, err := runRegexWriterVariant(t, context.Background(), []byte("a"), writer, false, []byte("x"), RegexOptions{
			WriteBufferSize: requested,
		})
		if !errors.Is(err, regexutil.ErrRegexResourceLimit) {
			t.Fatalf("error = %v, want ErrRegexResourceLimit", err)
		}
		if writer.writeCalls != 0 || writer.syncCalls != 0 || writer.Len() != 0 {
			t.Fatalf("invalid buffer touched output")
		}
	}
}

func runRegexWriterVariant(t *testing.T, ctx context.Context, source []byte, dst syncWriter, batch bool, replacement []byte, opts RegexOptions) (int64, int64, error) {
	t.Helper()
	sourcePath := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if batch {
		return ReplaceBatchRegexp(ctx, file, dst, []BatchRule{{Name: "dense", Find: []byte(`a`), Replace: replacement}}, opts)
	}
	matches, err := ReplaceRegexp(ctx, file, dst, []byte(`a`), replacement, opts)
	return matches, 0, err
}
