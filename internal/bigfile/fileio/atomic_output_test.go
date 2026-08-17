package fileio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicOutputPublishesCompleteSameDirectoryFile(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "result.txt")
	out, err := OpenAtomicOutput(finalPath, nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Cleanup()

	if filepath.Dir(out.TempPath()) != dir {
		t.Fatalf("temporary parent = %q, want %q", filepath.Dir(out.TempPath()), dir)
	}
	if _, err := out.Write([]byte("complete output")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final path visible before commit: %v", err)
	}
	tempPath := out.TempPath()
	if err := out.Commit(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(finalPath); err != nil || string(got) != "complete output" {
		t.Fatalf("final = %q, err %v", got, err)
	}
	if _, err := os.Lstat(tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file remains after commit: %v", err)
	}
}

func TestAtomicOutputCancellationPreservesAbsentFinal(t *testing.T) {
	finalPath := filepath.Join(t.TempDir(), "result.txt")
	out, err := OpenAtomicOutput(finalPath, nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("complete but canceled")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := out.CommitContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("commit error = %v, want context.Canceled", err)
	}
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled output reached final path: %v", err)
	}
	if err := out.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicOutputPublicationBoundaryCanRejectWithoutPublishing(t *testing.T) {
	finalPath := filepath.Join(t.TempDir(), "result.txt")
	out, err := OpenAtomicOutput(finalPath, nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Cleanup()
	if _, err := out.Write([]byte("complete but rejected")); err != nil {
		t.Fatal(err)
	}
	ctx := WithPublicationBoundary(context.Background(), func(func() error) error {
		return context.Canceled
	})
	if err := out.CommitContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("commit error = %v, want context.Canceled", err)
	}
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected boundary published output: %v", err)
	}
}

func TestPublicationBoundaryCannotBeReplacedByNestedEngine(t *testing.T) {
	outerCalls := 0
	innerCalls := 0
	ctx := WithPublicationBoundary(context.Background(), func(publish func() error) error {
		outerCalls++
		return publish()
	})
	ctx = WithPublicationBoundary(ctx, func(publish func() error) error {
		innerCalls++
		return publish()
	})
	if err := publishWithinBoundary(ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if outerCalls != 1 || innerCalls != 0 {
		t.Fatalf("boundary calls outer=%d inner=%d, want 1/0", outerCalls, innerCalls)
	}
}

func TestAtomicOutputRejectsSourceIdentityAlias(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.txt")
	aliasPath := filepath.Join(dir, "alias.txt")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sourcePath, aliasPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	if _, err := OpenAtomicOutput(aliasPath, []string{sourcePath}, 0o600); !errors.Is(err, ErrSourceAlias) {
		t.Fatalf("error = %v, want ErrSourceAlias", err)
	}
	if got, err := os.ReadFile(sourcePath); err != nil || string(got) != "source" {
		t.Fatalf("source changed: %q, %v", got, err)
	}
}

func TestAtomicOutputPublicationRaceDoesNotClobberCompetitor(t *testing.T) {
	finalPath := filepath.Join(t.TempDir(), "result.txt")
	out, err := OpenAtomicOutput(finalPath, nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Cleanup()
	if _, err := out.Write([]byte("operation output")); err != nil {
		t.Fatal(err)
	}

	restore := beforeAtomicOutputPublish
	beforeAtomicOutputPublish = func() {
		if err := os.WriteFile(finalPath, []byte("competitor"), 0o600); err != nil {
			t.Fatalf("create competing output: %v", err)
		}
	}
	defer func() { beforeAtomicOutputPublish = restore }()

	if err := out.Commit(); !errors.Is(err, ErrExists) {
		t.Fatalf("commit error = %v, want ErrExists", err)
	}
	if got, err := os.ReadFile(finalPath); err != nil || string(got) != "competitor" {
		t.Fatalf("competing output = %q, %v", got, err)
	}
}

func TestAtomicOutputRejectsNonExactPathWithoutCreatingCleanedTarget(t *testing.T) {
	dir := t.TempDir()
	finalPath := dir + string(os.PathSeparator) + "." + string(os.PathSeparator) + "result.txt"
	if _, err := OpenAtomicOutput(finalPath, nil, 0o600); !errors.Is(err, ErrInvalidExactPath) {
		t.Fatalf("error = %v, want ErrInvalidExactPath", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleaned target was created: %v", err)
	}
}
