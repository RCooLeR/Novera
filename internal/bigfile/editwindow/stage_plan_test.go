package editwindow

import "testing"

func TestPlanStagePatchForChangedPartialWindow(t *testing.T) {
	plan := PlanStage(StageRequest{
		Window:           Range{Start: 1_000, End: 2_000},
		FileSize:         10_000,
		BudgetBytes:      2_000,
		ReplacementBytes: 1_500,
		Changed:          true,
	})

	if plan.Action != StagePatch {
		t.Fatalf("Action = %v, want StagePatch: %s", plan.Action, plan.Message)
	}
	if plan.SourceRange != (Range{Start: 1_000, End: 2_000}) {
		t.Fatalf("SourceRange = %#v", plan.SourceRange)
	}
}

func TestPlanStageRejectsWholeFileAndOversizedReplacement(t *testing.T) {
	whole := PlanStage(StageRequest{
		Window:           Range{Start: 0, End: 100},
		FileSize:         100,
		BudgetBytes:      200,
		ReplacementBytes: 10,
		Changed:          true,
	})
	if whole.Action != StageReject {
		t.Fatalf("whole-file Action = %v, want StageReject", whole.Action)
	}

	oversized := PlanStage(StageRequest{
		Window:           Range{Start: 10, End: 20},
		FileSize:         100,
		BudgetBytes:      5,
		ReplacementBytes: 6,
		Changed:          true,
	})
	if oversized.Action != StageReject {
		t.Fatalf("oversized Action = %v, want StageReject", oversized.Action)
	}
}

func TestPlanStageReportsNoChange(t *testing.T) {
	plan := PlanStage(StageRequest{
		Window:           Range{Start: 10, End: 20},
		FileSize:         100,
		BudgetBytes:      20,
		ReplacementBytes: 10,
		Changed:          false,
	})
	if plan.Action != StageNoChange {
		t.Fatalf("Action = %v, want StageNoChange", plan.Action)
	}
}

func TestPlanStageConflictsWithExistingSourceRange(t *testing.T) {
	plan := PlanStage(StageRequest{
		Window:           Range{Start: 10, End: 20},
		FileSize:         100,
		BudgetBytes:      20,
		ReplacementBytes: 10,
		Changed:          true,
		ExistingModifiedRanges: []Range{
			{Start: 19, End: 22},
		},
	})
	if plan.Action != StageConflict {
		t.Fatalf("Action = %v, want StageConflict", plan.Action)
	}

	insertAtBoundary := PlanStage(StageRequest{
		Window:           Range{Start: 10, End: 20},
		FileSize:         100,
		BudgetBytes:      20,
		ReplacementBytes: 10,
		Changed:          true,
		ExistingModifiedRanges: []Range{
			{Start: 20, End: 20},
		},
	})
	if insertAtBoundary.Action != StagePatch {
		t.Fatalf("boundary insert Action = %v, want StagePatch", insertAtBoundary.Action)
	}
}

func TestPlanStageUpdatesLatestMatchingSourceRange(t *testing.T) {
	plan := PlanStage(StageRequest{
		Window:           Range{Start: 10, End: 20},
		FileSize:         100,
		BudgetBytes:      20,
		ReplacementBytes: 10,
		Changed:          true,
		ExistingModifiedRanges: []Range{
			{Start: 10, End: 20},
		},
		LastStagedSourceRange:    Range{Start: 10, End: 20},
		HasLastStagedSourceRange: true,
	})
	if plan.Action != StageUpdateExisting {
		t.Fatalf("Action = %v, want StageUpdateExisting", plan.Action)
	}
}

func TestPlanStageDoesNotUpdateAmbiguousOverlap(t *testing.T) {
	plan := PlanStage(StageRequest{
		Window:           Range{Start: 10, End: 20},
		FileSize:         100,
		BudgetBytes:      20,
		ReplacementBytes: 10,
		Changed:          true,
		ExistingModifiedRanges: []Range{
			{Start: 10, End: 25},
		},
		LastStagedSourceRange:    Range{Start: 10, End: 20},
		HasLastStagedSourceRange: true,
	})
	if plan.Action != StageConflict {
		t.Fatalf("Action = %v, want StageConflict", plan.Action)
	}
}
