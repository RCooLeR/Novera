package editwindow

import "testing"

func TestAroundKeepsWindowWithinBudgetAndFile(t *testing.T) {
	got, ok := Around(10_000, 9_700, 200, 2_000)
	if !ok {
		t.Fatal("Around returned !ok")
	}
	if got.Start != 8_000 || got.End != 10_000 {
		t.Fatalf("Around() = %+v, want 8000..10000", got)
	}
	if got.End-got.Start > 2_000 {
		t.Fatalf("window exceeds budget: %+v", got)
	}
}

func TestAroundRejectsViewportLargerThanBudget(t *testing.T) {
	if got, ok := Around(10_000, 500, 3_000, 2_000); ok {
		t.Fatalf("Around() = %+v, true; want rejection", got)
	}
}

func TestInitialSlicePlansBoundedStartOfHugeFile(t *testing.T) {
	got, ok := InitialSlice(10_000, 2_000)
	if !ok {
		t.Fatal("InitialSlice returned !ok")
	}
	if got.Start != 0 || got.End != 2_000 {
		t.Fatalf("InitialSlice() = %+v, want 0..2000", got)
	}
}

func TestInitialSliceForBudgetFittingFileCoversWholeFile(t *testing.T) {
	got, ok := InitialSlice(1_500, 2_000)
	if !ok {
		t.Fatal("InitialSlice returned !ok")
	}
	if got.Start != 0 || got.End != 1_500 {
		t.Fatalf("InitialSlice() = %+v, want 0..1500", got)
	}
}

func TestNextSlicePlansAdjacentBudgetWindow(t *testing.T) {
	got, ok := NextSlice(Range{Start: 0, End: 2_000}, 10_000, 2_000)
	if !ok {
		t.Fatal("NextSlice returned !ok")
	}
	if got.Start != 1_000 || got.End != 3_000 {
		t.Fatalf("NextSlice() = %+v, want 1000..3000", got)
	}
	if !got.Contains(Range{Start: 2_000, End: 2_001}) {
		t.Fatalf("NextSlice() = %+v does not contain adjacent byte", got)
	}
}

func TestPreviousSlicePlansAdjacentBudgetWindow(t *testing.T) {
	got, ok := PreviousSlice(Range{Start: 8_000, End: 10_000}, 10_000, 2_000)
	if !ok {
		t.Fatal("PreviousSlice returned !ok")
	}
	if got.Start != 6_999 || got.End != 8_999 {
		t.Fatalf("PreviousSlice() = %+v, want 6999..8999", got)
	}
	if !got.Contains(Range{Start: 7_999, End: 8_000}) {
		t.Fatalf("PreviousSlice() = %+v does not contain adjacent byte", got)
	}
}

func TestAdjacentSliceHelpersRejectWholeFileAndBoundaries(t *testing.T) {
	if got, ok := NextSlice(Range{Start: 0, End: 10_000}, 10_000, 2_000); ok {
		t.Fatalf("NextSlice() = %+v, true; want boundary rejection", got)
	}
	if got, ok := PreviousSlice(Range{Start: 0, End: 2_000}, 10_000, 2_000); ok {
		t.Fatalf("PreviousSlice() = %+v, true; want boundary rejection", got)
	}
	if got, ok := NextSlice(Range{Start: 0, End: 1_500}, 1_500, 2_000); ok {
		t.Fatalf("NextSlice() = %+v, true; want whole-file rejection", got)
	}
}
