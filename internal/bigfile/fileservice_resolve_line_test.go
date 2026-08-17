package bigfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novera/internal/bigfile/document"
)

func TestLineResolutionFromLookupDistinguishesPendingOffsetZeroAndAbsent(t *testing.T) {
	pending := lineResolutionFromLookup(document.LineStartLookupResult{
		Status:      document.LineStartLookupPending,
		Line:        1,
		Offset:      0,
		HasPosition: true,
	}, true)
	if !pending.Found || pending.Exact || pending.IndexComplete || pending.Limited ||
		pending.Offset != 0 || pending.ResolvedLine != 1 {
		t.Fatalf("pending resolution = %+v, want known line 1 at offset zero", pending)
	}

	absent := lineResolutionFromLookup(document.LineStartLookupResult{
		Status: document.LineStartLookupAbsent,
	}, false)
	if absent.Found || absent.Exact || !absent.IndexComplete || absent.Limited {
		t.Fatalf("absent resolution = %+v, want proven missing", absent)
	}
}

func TestResolveLineDistinguishesOffsetZeroAndMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)

	lineOne, err := service.ResolveLine(meta.FileID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !lineOne.Found || !lineOne.Exact || lineOne.Offset != 0 || lineOne.ResolvedLine != 1 || lineOne.Limited {
		t.Fatalf("line one resolution = %+v, want exact found offset zero", lineOne)
	}

	waitForResolveLineIndex(t, service, meta.FileID)
	missing, err := service.ResolveLine(meta.FileID, 99)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Found || !missing.IndexComplete || missing.Limited {
		t.Fatalf("missing-line resolution = %+v", missing)
	}
	lineTwo, err := service.ResolveLine(meta.FileID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !lineTwo.Found || !lineTwo.Exact || lineTwo.Offset != 4 || lineTwo.ResolvedLine != 2 || lineTwo.Limited {
		t.Fatalf("line two resolution = %+v, want exact offset four", lineTwo)
	}
}

func TestResolveLineReturnsBoundedFallbackForHugeLogicalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge-logical-line.txt")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	newlineOffset := resolveLineExactScanBytes + 1
	if err := file.Truncate(newlineOffset + 2); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{'\n', 'z'}, newlineOffset); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	waitForResolveLineIndex(t, service, meta.FileID)

	resolved, err := service.ResolveLine(meta.FileID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Found || resolved.Exact || !resolved.IndexComplete || !resolved.Limited {
		t.Fatalf("resolution = %+v, want explicit bounded fallback", resolved)
	}
	if resolved.Offset != 0 || resolved.ResolvedLine != 1 {
		t.Fatalf("fallback = %+v, want actual line 1 at offset zero", resolved)
	}
}

func TestResolveLinePropagatesChangedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	waitForResolveLineIndex(t, service, meta.FileID)

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveLine(meta.FileID, 2); !errors.Is(err, document.ErrSourceChanged) {
		t.Fatalf("error = %v, want document.ErrSourceChanged", err)
	}
}

func TestResolveLineRejectsInvalidInputAndHonorsCanceledContext(t *testing.T) {
	service := NewFileService()
	if _, err := service.ResolveLine("missing", 0); err == nil {
		t.Fatal("expected non-positive line error")
	}

	path := filepath.Join(t.TempDir(), "line.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.resolveLineContext(ctx, meta.FileID, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func waitForResolveLineIndex(t *testing.T, service *FileService, fileID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		file, ok := service.reg.Get(fileID)
		if !ok {
			t.Fatalf("opened session %q disappeared", fileID)
		}
		done := file.Doc.IndexProgress().Done
		file.Release()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("line index did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
