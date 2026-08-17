package bigfile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/document"
)

func TestVerifiedDocumentReaderRejectsChangedBlockBeforeReturningBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.bin")
	original := bytes.Repeat([]byte{'a'}, 2*int(sourceFingerprintChunkBytes))
	changed := append([]byte(nil), original...)
	changed[int(sourceFingerprintChunkBytes)+17] = 'b'
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	expected, err := captureDocumentSourceExpectation(context.Background(), doc, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := newVerifiedDocumentReader(context.Background(), expected, doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, 32)
	n, err := reader.ReadAt(got, sourceFingerprintChunkBytes)
	if n != 0 || !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("verified read = %d, %v; want no bytes and ErrOutputSourceChanged", n, err)
	}
	if !bytes.Equal(got, make([]byte, len(got))) {
		t.Fatalf("unverified bytes escaped to caller: %q", got)
	}
}

func TestSourceFingerprintLayoutHasAbsoluteBound(t *testing.T) {
	maximum := int64(maxSourceFingerprintChunks) * maxSourceFingerprintChunkBytes
	chunkSize, chunks, err := boundedSourceFingerprintLayout(maximum)
	if err != nil {
		t.Fatal(err)
	}
	if chunkSize != maxSourceFingerprintChunkBytes || chunks != maxSourceFingerprintChunks {
		t.Fatalf("maximum layout = chunk %d count %d", chunkSize, chunks)
	}
	if _, _, err := boundedSourceFingerprintLayout(maximum + 1); !errors.Is(err, ErrOutputSourceVerificationLimit) {
		t.Fatalf("oversized layout error = %v, want ErrOutputSourceVerificationLimit", err)
	}
}

func TestSourceFingerprintProgressIsBoundedMonotonicAndFinal(t *testing.T) {
	const total = int64(100_003)
	updates := make([]int64, 0, maxSourceFingerprintProgress)
	reporter := newSourceFingerprintProgressReporter(total, func(completed, gotTotal int64) {
		if gotTotal != total {
			t.Fatalf("progress total = %d, want %d", gotTotal, total)
		}
		updates = append(updates, completed)
	})
	for completed := int64(0); completed <= total; completed++ {
		reporter.report(completed)
	}
	reporter.report(total)
	if len(updates) == 0 || int64(len(updates)) > maxSourceFingerprintProgress {
		t.Fatalf("progress callbacks = %d, want 1..%d", len(updates), maxSourceFingerprintProgress)
	}
	previous := int64(-1)
	for _, completed := range updates {
		if completed <= previous || completed < 0 || completed > total {
			t.Fatalf("non-monotonic progress after %d: %d", previous, completed)
		}
		previous = completed
	}
	if previous != total {
		t.Fatalf("final progress = %d, want %d", previous, total)
	}
}
