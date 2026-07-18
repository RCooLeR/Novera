package bigfile

import (
	"bytes"
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

func TestSearchAllClampsMaxIntHitBudget(t *testing.T) {
	data := bytes.Repeat([]byte("x\n"), maxSearchAllHits+250)
	svc, meta := openBoundTestFile(t, "search.txt", data)
	got, err := svc.SearchAll(meta.FileID, "x", false, true, false, int(^uint(0)>>1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hits) != maxSearchAllHits {
		t.Fatalf("hits = %d, want capped %d", len(got.Hits), maxSearchAllHits)
	}
	if !got.Truncated {
		t.Fatal("capped search must report truncation")
	}
}

func TestBridgeWindowsClampMaxIntByteBudgets(t *testing.T) {
	maxInt := int(^uint(0) >> 1)

	t.Run("csv grid", func(t *testing.T) {
		svc, meta := openBoundTestFile(t, "grid.csv", bytes.Repeat([]byte("a"), maxCSVGridBytes+1024))
		got, err := svc.GetCsvGrid(meta.FileID, ",", 0, maxInt)
		if err != nil {
			t.Fatal(err)
		}
		if got.NextByte > maxCSVGridBytes {
			t.Fatalf("next byte = %d, cap = %d", got.NextByte, maxCSVGridBytes)
		}
	})

	t.Run("edit and diff", func(t *testing.T) {
		data := bytes.Repeat([]byte("a"), maxEditWindowBytes+1024)
		svc, meta := openBoundTestFile(t, "edit.txt", data)
		edit, err := svc.GetEditWindow(meta.FileID, 0, maxInt)
		if err != nil {
			t.Fatal(err)
		}
		if len(edit.Text) > maxEditWindowBytes || edit.NextByte > maxEditWindowBytes {
			t.Fatalf("edit window bytes = %d, next = %d", len(edit.Text), edit.NextByte)
		}
		diff, err := svc.GetDiffWindow(meta.FileID, 0, maxInt)
		if err != nil {
			t.Fatal(err)
		}
		if len(diff.Edited) > maxEditWindowBytes || len(diff.Original) > maxEditWindowBytes || diff.NextByte > maxEditWindowBytes {
			t.Fatalf("diff bytes = %d/%d, next = %d", len(diff.Edited), len(diff.Original), diff.NextByte)
		}
	})

	t.Run("hex", func(t *testing.T) {
		svc, meta := openBoundTestFile(t, "binary.bin", bytes.Repeat([]byte{0xaa}, maxHexWindowBytes+1024))
		got, err := svc.GetHexWindow(meta.FileID, 0, maxInt)
		if err != nil {
			t.Fatal(err)
		}
		if got.NextByte > maxHexWindowBytes {
			t.Fatalf("next byte = %d, cap = %d", got.NextByte, maxHexWindowBytes)
		}
	})
}
