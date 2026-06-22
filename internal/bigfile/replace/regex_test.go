package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewRegexp(t *testing.T) {
	r := memReaderAt{data: []byte("alpha abc-123 bravo\ncharlie def-456 delta")}

	previews, err := PreviewRegexp(context.Background(), r, []byte(`([a-z]+)-([0-9]+)`), []byte(`${2}:${1}`), RegexPreviewOptions{
		ChunkSize:    8,
		MaxHits:      2,
		PreviewBytes: 6,
	}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 2 {
		t.Fatalf("len(previews) = %d, want 2", len(previews))
	}
	if previews[0].After != "alpha 123:abc bravo" {
		t.Fatalf("first after = %q", previews[0].After)
	}
}

func TestReplaceRegexpBoundaryAndCaptures(t *testing.T) {
	src, err := os.CreateTemp("", "q-regex-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-regex-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("abc xx12 yy34"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	matches, err := ReplaceRegexp(context.Background(), src, dst, []byte(`([a-z]{2})([0-9]{2})`), []byte(`${2}-${1}`), RegexOptions{
		ChunkSize:      5,
		MaxMatchWindow: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 2 {
		t.Fatalf("matches = %d, want 2", matches)
	}

	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc 12-xx 34-yy" {
		t.Fatalf("got %q, want %q", string(got), "abc 12-xx 34-yy")
	}
}

func TestReplaceRegexpRejectsEmptyMatchPatterns(t *testing.T) {
	src, err := os.CreateTemp("", "q-regex-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-regex-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := ReplaceRegexp(context.Background(), src, dst, []byte(`a*`), []byte(``), RegexOptions{}); err == nil {
		t.Fatal("expected empty-match regex rejection")
	}
}

func TestPreviewRegexpRejectsEmptyMatchPatterns(t *testing.T) {
	r := memReaderAt{data: []byte("hello")}
	if _, err := PreviewRegexp(context.Background(), r, []byte(`\b`), []byte(`|`), RegexPreviewOptions{}, RegexOptions{}); err == nil {
		t.Fatal("expected empty-match regex rejection")
	}
}

func TestReplaceRegexpFileWritesOutputAndManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("aa12 bb34"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ReplaceRegexpFile(context.Background(), srcPath, outPath, []byte(`([a-z]{2})([0-9]{2})`), []byte(`${2}-${1}`), FileOptions{
		ChunkSize: 4,
	}, RegexOptions{
		ChunkSize:      4,
		MaxMatchWindow: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != 2 {
		t.Fatalf("matches = %d, want 2", summary.Matches)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "12-aa 34-bb" {
		t.Fatalf("output = %q", string(got))
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Operation != "regex-replace" {
		t.Fatalf("manifest operation = %q", manifest.Operation)
	}
	if manifest.Status != "complete" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
}

func TestReplaceRegexpFileCancelDeletesPartialWhenRequested(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("ab12 ", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ReplaceRegexpFile(ctx, srcPath, outPath, []byte(`([a-z]{2})([0-9]{2})`), []byte(`${2}-${1}`), FileOptions{
		ChunkSize:             8,
		DeletePartialOnCancel: true,
	}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
		Progress: func(Progress) {
			if !canceled {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial temp should be deleted, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
}

func TestReplaceRegexpFileCancelKeepsPartialWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("ab12 ", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ReplaceRegexpFile(ctx, srcPath, outPath, []byte(`([a-z]{2})([0-9]{2})`), []byte(`${2}-${1}`), FileOptions{
		ChunkSize:             8,
		DeletePartialOnCancel: false,
	}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
		Progress: func(Progress) {
			if !canceled {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); err != nil {
		t.Fatalf("partial temp should be preserved, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if !strings.Contains(manifest.Error, context.Canceled.Error()) {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}
