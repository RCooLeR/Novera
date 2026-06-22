package replace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEstimatePlainOutputSizeCountsGrowth(t *testing.T) {
	r := memReaderAt{data: []byte("x x")}

	estimate, err := EstimatePlainOutputSize(context.Background(), r, []byte("x"), []byte("long"), SpaceOptions{ChunkSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if estimate.OutputSize != int64(len("long long")) {
		t.Fatalf("output size = %d, want %d", estimate.OutputSize, len("long long"))
	}
	if estimate.Matches != 2 {
		t.Fatalf("matches = %d, want 2", estimate.Matches)
	}
	if !estimate.CountedMatches {
		t.Fatal("expected counted matches")
	}
}

func TestEstimatePlainOutputSizeCountsShrink(t *testing.T) {
	r := memReaderAt{data: []byte("hello world hello")}

	estimate, err := EstimatePlainOutputSize(context.Background(), r, []byte("hello"), []byte("hi"), SpaceOptions{ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if estimate.OutputSize != int64(len("hi world hi")) {
		t.Fatalf("output size = %d, want %d", estimate.OutputSize, len("hi world hi"))
	}
	if estimate.Matches != 2 {
		t.Fatalf("matches = %d, want 2", estimate.Matches)
	}
}

func TestEstimatePlainOutputSizeSkipsCountingSameLength(t *testing.T) {
	r := memReaderAt{data: []byte("hello hello")}

	estimate, err := EstimatePlainOutputSize(context.Background(), r, []byte("hello"), []byte("world"), SpaceOptions{ChunkSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if estimate.OutputSize != int64(len("hello hello")) {
		t.Fatalf("output size = %d, want %d", estimate.OutputSize, len("hello hello"))
	}
	if estimate.CountedMatches {
		t.Fatal("same-length replacement should not require a count")
	}
}

func TestCheckPlainReplaceSpaceAddsSafetyBytes(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	estimate, err := CheckPlainReplaceSpace(context.Background(), srcPath, outPath, []byte("hello"), []byte("bye"), SpaceOptions{
		ChunkSize:   2,
		SafetyBytes: 123,
	})
	if err != nil {
		t.Fatal(err)
	}
	if estimate.OutputSize != int64(len("bye")) {
		t.Fatalf("output size = %d, want %d", estimate.OutputSize, len("bye"))
	}
	if estimate.RequiredBytes != estimate.OutputSize+123 {
		t.Fatalf("required bytes = %d, want %d", estimate.RequiredBytes, estimate.OutputSize+123)
	}
}
