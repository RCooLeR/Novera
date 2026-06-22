package editwindow

import "testing"

func TestPlanOpenAutoLoadsSmallFullBuffer(t *testing.T) {
	got := PlanOpen(OpenRequest{
		FileSize:            4 * 1024,
		EditableBudgetBytes: 10 * 1024,
		SmallAutoLoadBytes:  8 * 1024,
	})
	if got.Mode != OpenModeFullBufferAuto {
		t.Fatalf("Mode = %v, want OpenModeFullBufferAuto", got.Mode)
	}
}

func TestPlanOpenKeepsBudgetFittingLargeFileOnDemand(t *testing.T) {
	got := PlanOpen(OpenRequest{
		FileSize:            9 * 1024,
		EditableBudgetBytes: 10 * 1024,
		SmallAutoLoadBytes:  8 * 1024,
	})
	if got.Mode != OpenModeFullBufferOnDemand {
		t.Fatalf("Mode = %v, want OpenModeFullBufferOnDemand", got.Mode)
	}
}

func TestPlanOpenAutoLoadsOnlyFirstSliceForHugeFile(t *testing.T) {
	got := PlanOpen(OpenRequest{
		FileSize:            100 * 1024,
		EditableBudgetBytes: 10 * 1024,
		SmallAutoLoadBytes:  8 * 1024,
	})
	if got.Mode != OpenModeSliceAuto {
		t.Fatalf("Mode = %v, want OpenModeSliceAuto", got.Mode)
	}
	if got.Slice.Start != 0 || got.Slice.End != 10*1024 {
		t.Fatalf("Slice = %+v, want first budget-bounded slice", got.Slice)
	}
	if got.Slice.End-got.Slice.Start > 10*1024 {
		t.Fatalf("slice exceeds budget: %+v", got.Slice)
	}
}

func TestPlanOpenNeverLoadsBinaryIntoEditor(t *testing.T) {
	got := PlanOpen(OpenRequest{
		FileSize:            4 * 1024,
		EditableBudgetBytes: 10 * 1024,
		SmallAutoLoadBytes:  8 * 1024,
		Binary:              true,
	})
	if got.Mode != OpenModeMetadataOnly {
		t.Fatalf("Mode = %v, want OpenModeMetadataOnly", got.Mode)
	}
}

func TestPlanOpenRejectsMissingBudget(t *testing.T) {
	got := PlanOpen(OpenRequest{
		FileSize:            100 * 1024,
		EditableBudgetBytes: 0,
		SmallAutoLoadBytes:  8 * 1024,
	})
	if got.Mode != OpenModeBudgetTooSmall {
		t.Fatalf("Mode = %v, want OpenModeBudgetTooSmall", got.Mode)
	}
}
