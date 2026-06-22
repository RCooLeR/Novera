package replace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/encodingx"
)

func TestReplacePlainFileReadyManifestFailurePreservesSourceAndTemp(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	source := "hello world hello"
	if err := os.WriteFile(srcPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	manifestErr := errors.New("ready manifest write failed")
	restore := failReplaceManifestOpenOnCall(t, 2, manifestErr)
	defer restore()

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{ChunkSize: 5})
	if !errors.Is(err, manifestErr) {
		t.Fatalf("err = %v, want ready manifest failure", err)
	}
	assertReplaceFileContent(t, srcPath, source)
	if _, statErr := os.Stat(outPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output stat err = %v, want no final output before ready manifest", statErr)
	}
	assertReplaceFileContent(t, summary.TempPath, "bye world bye")

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "failed" || !strings.Contains(manifest.Error, manifestErr.Error()) {
		t.Fatalf("manifest status/error = %q/%q, want failed ready-manifest evidence", manifest.Status, manifest.Error)
	}
}

func TestReplacePlainFileCompletedManifestFailurePreservesSourceAndOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	source := "hello world hello"
	if err := os.WriteFile(srcPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	manifestErr := errors.New("completed manifest write failed")
	restore := failReplaceManifestOpenOnCall(t, 3, manifestErr)
	defer restore()

	summary, err := ReplacePlainFile(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), FileOptions{ChunkSize: 5})
	if !errors.Is(err, manifestErr) {
		t.Fatalf("err = %v, want completed manifest failure", err)
	}
	assertReplaceFileContent(t, srcPath, source)
	assertReplaceFileContent(t, outPath, "bye world bye")
	if _, statErr := os.Stat(summary.TempPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temp stat err = %v, want temp promoted to output", statErr)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "failed" || !strings.Contains(manifest.Error, manifestErr.Error()) {
		t.Fatalf("manifest status/error = %q/%q, want failed completed-manifest evidence", manifest.Status, manifest.Error)
	}
}

func TestReplaceTransformReadyManifestFailurePreservesSourceAndTemp(t *testing.T) {
	tests := []struct {
		name       string
		source     []byte
		run        func(context.Context, string, string) (FileSummary, error)
		assertTemp func(*testing.T, string)
	}{
		{
			name:   "regex",
			source: []byte("aa11 bb22\n"),
			run: func(ctx context.Context, srcPath string, outPath string) (FileSummary, error) {
				return ReplaceRegexpFile(ctx, srcPath, outPath, []byte(`([a-z]{2})([0-9]{2})`), []byte(`${2}-${1}`), FileOptions{ChunkSize: 5}, RegexOptions{
					ChunkSize:      5,
					MaxMatchWindow: 16,
				})
			},
			assertTemp: func(t *testing.T, path string) {
				t.Helper()
				assertReplaceFileContent(t, path, "11-aa 22-bb\n")
			},
		},
		{
			name:   "line-ending",
			source: []byte("alpha\r\nbeta\r\n"),
			run: func(ctx context.Context, srcPath string, outPath string) (FileSummary, error) {
				return ConvertLineEndingsFile(ctx, srcPath, outPath, "LF", FileOptions{ChunkSize: 5})
			},
			assertTemp: func(t *testing.T, path string) {
				t.Helper()
				assertReplaceFileContent(t, path, "alpha\nbeta\n")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			srcPath := filepath.Join(dir, "source.txt")
			outPath := filepath.Join(dir, "output.txt")
			if err := os.WriteFile(srcPath, tt.source, 0o600); err != nil {
				t.Fatal(err)
			}

			manifestErr := errors.New("ready manifest write failed")
			restore := failReplaceManifestOpenOnCall(t, 2, manifestErr)
			defer restore()

			summary, err := tt.run(context.Background(), srcPath, outPath)
			if !errors.Is(err, manifestErr) {
				t.Fatalf("err = %v, want ready manifest failure", err)
			}
			assertReplaceFileBytes(t, srcPath, tt.source)
			if _, statErr := os.Stat(outPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("output stat err = %v, want no final output before ready manifest", statErr)
			}
			tt.assertTemp(t, summary.TempPath)

			manifest := readManifest(t, summary.ManifestPath)
			if manifest.Status != "failed" || !strings.Contains(manifest.Error, manifestErr.Error()) {
				t.Fatalf("manifest status/error = %q/%q, want failed ready-manifest evidence", manifest.Status, manifest.Error)
			}
		})
	}
}

func TestReplaceTransformCompletedManifestFailurePreservesSourceAndOutput(t *testing.T) {
	tests := []struct {
		name         string
		source       []byte
		run          func(context.Context, string, string) (FileSummary, error)
		assertOutput func(*testing.T, string)
	}{
		{
			name:   "regex",
			source: []byte("aa11 bb22\n"),
			run: func(ctx context.Context, srcPath string, outPath string) (FileSummary, error) {
				return ReplaceRegexpFile(ctx, srcPath, outPath, []byte(`([a-z]{2})([0-9]{2})`), []byte(`${2}-${1}`), FileOptions{ChunkSize: 5}, RegexOptions{
					ChunkSize:      5,
					MaxMatchWindow: 16,
				})
			},
			assertOutput: func(t *testing.T, path string) {
				t.Helper()
				assertReplaceFileContent(t, path, "11-aa 22-bb\n")
			},
		},
		{
			name:   "batch-plain",
			source: []byte("hello world hello\n"),
			run: func(ctx context.Context, srcPath string, outPath string) (FileSummary, error) {
				rules := []BatchRule{
					{Name: "Greeting", Find: []byte("hello"), Replace: []byte("bye"), Priority: 0},
					{Name: "Place", Find: []byte("world"), Replace: []byte("earth"), Priority: 1},
				}
				return ReplaceBatchPlainFile(ctx, srcPath, outPath, rules, FileOptions{ChunkSize: 5}, BatchOptions{ChunkSize: 5})
			},
			assertOutput: func(t *testing.T, path string) {
				t.Helper()
				assertReplaceFileContent(t, path, "bye earth bye\n")
			},
		},
		{
			name:   "batch-regex",
			source: []byte("id=41 id=42\n"),
			run: func(ctx context.Context, srcPath string, outPath string) (FileSummary, error) {
				rules := []BatchRule{
					{Name: "ID", Find: []byte(`id=(\d\d)`), Replace: []byte(`row-$1`), Priority: 0},
				}
				return ReplaceBatchRegexpFile(ctx, srcPath, outPath, rules, FileOptions{ChunkSize: 5}, RegexOptions{
					ChunkSize:      5,
					MaxMatchWindow: 16,
				})
			},
			assertOutput: func(t *testing.T, path string) {
				t.Helper()
				assertReplaceFileContent(t, path, "row-41 row-42\n")
			},
		},
		{
			name:   "line-ending",
			source: []byte("alpha\r\nbeta\r\n"),
			run: func(ctx context.Context, srcPath string, outPath string) (FileSummary, error) {
				return ConvertLineEndingsFile(ctx, srcPath, outPath, "LF", FileOptions{ChunkSize: 5})
			},
			assertOutput: func(t *testing.T, path string) {
				t.Helper()
				assertReplaceFileContent(t, path, "alpha\nbeta\n")
			},
		},
		{
			name:   "encoding",
			source: []byte("alpha\nbeta\n"),
			run: func(ctx context.Context, srcPath string, outPath string) (FileSummary, error) {
				return ConvertEncodingFile(ctx, srcPath, outPath, "UTF-16LE", FileOptions{ChunkSize: 5})
			},
			assertOutput: func(t *testing.T, path string) {
				t.Helper()
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) < 2 || got[0] != 0xFF || got[1] != 0xFE {
					t.Fatalf("output BOM = %v, want UTF-16LE BOM", got[:min(2, len(got))])
				}
				decoded, err := encodingx.DecodeBytes("UTF-16LE", got)
				if err != nil {
					t.Fatal(err)
				}
				if decoded != "alpha\nbeta\n" {
					t.Fatalf("decoded output = %q", decoded)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			srcPath := filepath.Join(dir, "source.txt")
			outPath := filepath.Join(dir, "output.txt")
			if err := os.WriteFile(srcPath, tt.source, 0o600); err != nil {
				t.Fatal(err)
			}

			manifestErr := errors.New("completed manifest write failed")
			restore := failReplaceManifestOpenOnCall(t, 3, manifestErr)
			defer restore()

			summary, err := tt.run(context.Background(), srcPath, outPath)
			if !errors.Is(err, manifestErr) {
				t.Fatalf("err = %v, want completed manifest failure", err)
			}
			assertReplaceFileBytes(t, srcPath, tt.source)
			tt.assertOutput(t, outPath)
			if _, statErr := os.Stat(summary.TempPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("temp stat err = %v, want temp promoted to output", statErr)
			}

			manifest := readManifest(t, summary.ManifestPath)
			if manifest.Status != "failed" || !strings.Contains(manifest.Error, manifestErr.Error()) {
				t.Fatalf("manifest status/error = %q/%q, want failed completed-manifest evidence", manifest.Status, manifest.Error)
			}
		})
	}
}

func failReplaceManifestOpenOnCall(t *testing.T, failCall int, err error) func() {
	t.Helper()
	original := openManifestOut
	calls := 0
	openManifestOut = func(path string, exclusive bool) (io.WriteCloser, error) {
		calls++
		if calls == failCall {
			return nil, err
		}
		return original(path, exclusive)
	}
	return func() {
		openManifestOut = original
	}
}

func assertReplaceFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}

func assertReplaceFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", path, string(got), string(want))
	}
}
