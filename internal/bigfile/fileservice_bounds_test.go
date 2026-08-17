package bigfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func openBoundTestFile(t *testing.T, name string, data []byte) (*FileService, FileMeta) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewFileService()
	meta, err := svc.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.CloseFile(meta.FileID) })
	return svc, meta
}

func TestClampRequestIntBoundaries(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name  string
		value int
		want  int
	}{
		{name: "negative uses default", value: -1, want: 7},
		{name: "zero uses default", value: 0, want: 7},
		{name: "positive preserved", value: 8, want: 8},
		{name: "maximum preserved", value: 11, want: 11},
		{name: "max int clamped", value: maxInt, want: 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampRequestInt(tc.value, 7, 11); got != tc.want {
				t.Fatalf("clampRequestInt(%d) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestBoundedReadArithmeticRejectsOverflow(t *testing.T) {
	if got := boundedReadEnd(maxInt64Value-2, maxInt64Value, 100); got != maxInt64Value {
		t.Fatalf("bounded end = %d", got)
	}
	if _, ok := checkedRangeEnd(maxInt64Value, 1); ok {
		t.Fatal("overflowing range was accepted")
	}
	if _, ok := checkedRangeEnd(-1, 1); ok {
		t.Fatal("negative range was accepted")
	}
}

func TestStageEditRejectsUnboundedAndOverflowingRanges(t *testing.T) {
	svc, meta := openBoundTestFile(t, "stage.txt", []byte("small"))
	if _, err := svc.StageEdit(meta.FileID, maxInt64Value, 1, "x"); err == nil {
		t.Fatal("overflowing edit range was accepted")
	}
	if _, err := svc.StageEdit(meta.FileID, 0, int64(maxEditWindowBytes)+1, "x"); err == nil {
		t.Fatal("oversized original range was accepted")
	}
	if _, err := svc.StageEdit(meta.FileID, 0, 0, string(make([]byte, maxEditWindowBytes+1))); err == nil {
		t.Fatal("oversized replacement was accepted")
	}
}

func TestSearchAllRequestRejectsMaxIntHitBudget(t *testing.T) {
	data := bytes.Repeat([]byte("x\n"), maxSearchAllHits+250)
	svc, meta := openBoundTestFile(t, "search.txt", data)
	requestID, err := svc.BeginSearchRequest(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SearchAllRequest(requestID, "x", false, true, false, int(^uint(0)>>1)); err == nil {
		t.Fatal("SearchAllRequest accepted an unbounded result limit")
	}
}

func TestBridgeWindowsRejectMaxIntByteBudgets(t *testing.T) {
	maxInt := int(^uint(0) >> 1)

	t.Run("csv grid", func(t *testing.T) {
		svc, meta := openBoundTestFile(t, "grid.csv", bytes.Repeat([]byte("a"), maxCSVGridBytes+1024))
		if _, err := svc.GetCsvGrid(meta.FileID, ",", 0, maxInt); err == nil {
			t.Fatalf("CSV grid accepted byte budget above hard cap %d", maxCSVGridBytes)
		}
	})

	t.Run("edit and diff", func(t *testing.T) {
		svc, meta := openBoundTestFile(t, "edit.txt", []byte("alpha\n"))
		if _, err := svc.GetEditWindow(meta.FileID, 0, maxInt); !errors.Is(err, ErrEditRequestTooLarge) {
			t.Fatalf("edit oversized request error = %v, want ErrEditRequestTooLarge", err)
		}
		if _, err := svc.GetDiffWindow(meta.FileID, 0, maxInt); !errors.Is(err, ErrEditRequestTooLarge) {
			t.Fatalf("diff oversized request error = %v, want ErrEditRequestTooLarge", err)
		}
	})

	t.Run("hex", func(t *testing.T) {
		svc, meta := openBoundTestFile(t, "binary.bin", bytes.Repeat([]byte{0xaa}, maxHexWindowBytes+1024))
		if _, err := svc.GetHexWindow(meta.FileID, 0, maxInt); !errors.Is(err, ErrHexRequestTooLarge) {
			t.Fatalf("oversized request error = %v, want ErrHexRequestTooLarge", err)
		}
		got, err := svc.GetHexWindow(meta.FileID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got.NextByte > maxHexWindowBytes {
			t.Fatalf("default next byte = %d, cap = %d", got.NextByte, maxHexWindowBytes)
		}
		if got.FileID != meta.FileID {
			t.Fatalf("hex window file id = %q, want %q", got.FileID, meta.FileID)
		}
	})
}

func TestHexWindowRejectsNegativeOffsetsAndBudgets(t *testing.T) {
	svc, meta := openBoundTestFile(t, "binary.bin", []byte{0xaa, 0xbb})
	for _, test := range []struct {
		name     string
		start    int64
		maxBytes int
	}{
		{name: "negative offset", start: -1, maxBytes: 1},
		{name: "negative budget", start: 0, maxBytes: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := svc.GetHexWindow(meta.FileID, test.start, test.maxBytes); !errors.Is(err, ErrHexRequestTooLarge) {
				t.Fatalf("error = %v, want ErrHexRequestTooLarge", err)
			}
		})
	}
}

func TestCSVServiceRejectsMultiRuneDelimiterInsteadOfDefaultingToComma(t *testing.T) {
	svc, meta := openBoundTestFile(t, "delimiter.csv", []byte("a,b\n1,2\n"))
	if _, err := svc.CsvPreview(meta.FileID, "ab", true, 10); err == nil {
		t.Fatal("multi-rune delimiter silently defaulted to comma")
	}
}

func TestCSVSampleReaderIncludesTruncationLookaheadByte(t *testing.T) {
	data := bytes.Repeat([]byte{'x'}, int(csvSampleBytes+2))
	svc, meta := openBoundTestFile(t, "lookahead.csv", data)
	reader, _, err := svc.csvSampleReader(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(got)) != csvSampleBytes+1 {
		t.Fatalf("sample bytes = %d, want %d including lookahead", len(got), csvSampleBytes+1)
	}
}
