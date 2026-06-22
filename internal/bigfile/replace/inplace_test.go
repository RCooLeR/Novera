package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplacePlainFileInPlaceDisabledPreservesSource(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(srcPath, []byte("alpha beta alpha"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ReplacePlainFileInPlace(context.Background(), srcPath, []byte("alpha"), []byte("omega"), InPlaceOptions{ChunkSize: 4})
	if !errors.Is(err, ErrInPlacePatchDisabled) {
		t.Fatalf("err = %v, want ErrInPlacePatchDisabled", err)
	}
	if summary.Patched {
		t.Fatal("Patched = true, want false")
	}
	if summary.Matches != 0 || summary.BytesPatched != 0 {
		t.Fatalf("summary recorded mutations: matches=%d bytes=%d", summary.Matches, summary.BytesPatched)
	}

	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha beta alpha" {
		t.Fatalf("source = %q", string(got))
	}
	if _, err := os.Stat(summary.ManifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest stat err = %v, want not exist", err)
	}
}

func TestReplacePlainFileInPlaceRejectsDifferentLength(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ReplacePlainFileInPlace(context.Background(), srcPath, []byte("hello"), []byte("bye"), InPlaceOptions{}); err == nil {
		t.Fatal("expected same-length guard")
	}

	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("source changed to %q", string(got))
	}
}

func TestReplacePlainFileInPlaceCanceledContextStillPreservesSource(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(srcPath, []byte("hello hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summary, err := ReplacePlainFileInPlace(ctx, srcPath, []byte("hello"), []byte("hullo"), InPlaceOptions{
		ChunkSize: 32,
	})
	if !errors.Is(err, ErrInPlacePatchDisabled) {
		t.Fatalf("err = %v, want ErrInPlacePatchDisabled", err)
	}

	if _, err := os.Stat(summary.ManifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest stat err = %v, want not exist", err)
	}

	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello hello" {
		t.Fatalf("source changed to %q", got)
	}
}
