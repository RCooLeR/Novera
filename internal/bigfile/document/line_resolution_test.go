package document

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLookupLineStartPreservesOffsetZeroAndPendingIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending-line-index.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	lineOne, err := doc.LookupLineStart(context.Background(), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if lineOne.Status != LineStartLookupExact || !lineOne.HasPosition || lineOne.Line != 1 || lineOne.Offset != 0 {
		t.Fatalf("line one lookup = %#v, want exact position at offset zero", lineOne)
	}

	pending, err := doc.LookupLineStart(context.Background(), 2, exactScanChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != LineStartLookupPending || pending.HasPosition {
		t.Fatalf("pending lookup = %#v, want unavailable incomplete index", pending)
	}
}

func TestLookupLineStartHardCapReturnsHonestHugeLineFallback(t *testing.T) {
	const budget = int64(exactScanChunkSize + 37)
	content := bytes.Repeat([]byte{'x'}, int(budget+1))
	content = append(content, '\n', 'z')
	path := filepath.Join(t.TempDir(), "huge-first-line.txt")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	if err := doc.StartIndexing(context.Background()); err != nil {
		t.Fatal(err)
	}

	var requestedBytes int64
	var readCalls int
	lookup, err := doc.lookupLineStart(context.Background(), 2, budget, func(p []byte, off int64) (int, error) {
		readCalls++
		requestedBytes += int64(len(p))
		return doc.ReadAt(p, off)
	})
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Status != LineStartLookupLimited || !lookup.HasPosition || lookup.Line != 1 || lookup.Offset != 0 {
		t.Fatalf("lookup = %#v, want limited fallback to exact line 1 offset 0", lookup)
	}
	if requestedBytes != budget {
		t.Fatalf("requested source bytes = %d, want hard cap %d", requestedBytes, budget)
	}
	if readCalls != 2 {
		t.Fatalf("read calls = %d, want one full chunk plus one capped remainder", readCalls)
	}
}

func TestLookupLineStartCancellationReturnsFallbackWithoutAnotherRead(t *testing.T) {
	content := bytes.Repeat([]byte{'x'}, 2*exactScanChunkSize)
	content = append(content, '\n', 'z')
	path := filepath.Join(t.TempDir(), "cancel-exact-line.txt")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	if err := doc.StartIndexing(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requestedBytes int64
	var readCalls int
	lookup, err := doc.lookupLineStart(ctx, 2, int64(len(content)), func(p []byte, off int64) (int, error) {
		readCalls++
		requestedBytes += int64(len(p))
		n, readErr := doc.ReadAt(p, off)
		cancel()
		return n, readErr
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if lookup.Status != LineStartLookupLimited || !lookup.HasPosition || lookup.Line != 1 || lookup.Offset != 0 {
		t.Fatalf("lookup = %#v, want cancellation fallback to line 1 offset 0", lookup)
	}
	if readCalls != 1 || requestedBytes != exactScanChunkSize {
		t.Fatalf("reads = %d calls, %d bytes; want one %d-byte read", readCalls, requestedBytes, exactScanChunkSize)
	}
}

func TestLookupLineStartCapEdgeEOFAndProvenMissing(t *testing.T) {
	content := []byte("abc\n")
	path := filepath.Join(t.TempDir(), "line-at-eof.txt")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	if err := doc.StartIndexing(context.Background()); err != nil {
		t.Fatal(err)
	}

	limited, err := doc.LookupLineStart(context.Background(), 2, int64(len(content)-1))
	if err != nil {
		t.Fatal(err)
	}
	if limited.Status != LineStartLookupLimited || limited.Line != 1 || limited.Offset != 0 {
		t.Fatalf("below-edge lookup = %#v, want limited line 1 fallback", limited)
	}

	exact, err := doc.LookupLineStart(context.Background(), 2, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if exact.Status != LineStartLookupExact || !exact.HasPosition || exact.Line != 2 || exact.Offset != int64(len(content)) {
		t.Fatalf("at-edge lookup = %#v, want exact empty line 2 at EOF", exact)
	}

	readCalls := 0
	missing, err := doc.lookupLineStart(context.Background(), 3, 0, func([]byte, int64) (int, error) {
		readCalls++
		return 0, errors.New("unexpected read")
	})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Status != LineStartLookupAbsent || missing.HasPosition {
		t.Fatalf("missing lookup = %#v, want line proven absent", missing)
	}
	if readCalls != 0 {
		t.Fatalf("proven-missing lookup issued %d source reads", readCalls)
	}
}

func TestLookupLineStartRejectsInvalidInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "line.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	if _, err := doc.LookupLineStart(context.Background(), 0, 1); err == nil {
		t.Fatal("expected non-positive line error")
	}
	if _, err := doc.LookupLineStart(context.Background(), 1, -1); err == nil {
		t.Fatal("expected negative budget error")
	}
}

func TestLookupLineStartRejectsChangedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changed.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	if err := doc.StartIndexing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := doc.LookupLineStart(context.Background(), 2, exactScanChunkSize); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("error = %v, want ErrSourceChanged", err)
	}
}
