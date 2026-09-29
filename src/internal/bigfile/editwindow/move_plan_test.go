package editwindow

import (
	"strings"
	"testing"
)

func TestPlanMoveKeepsCoveredViewport(t *testing.T) {
	plan := PlanMove(MoveRequest{
		FileSize:      10_000,
		Current:       Range{Start: 1_000, End: 3_000},
		TargetOffset:  1_500,
		ViewportBytes: 200,
		BudgetBytes:   2_000,
		Dirty:         true,
	})
	if plan.Action != SlideStay {
		t.Fatalf("Action = %v, want SlideStay", plan.Action)
	}
	if plan.Next != (Range{}) {
		t.Fatalf("Next = %+v, want empty", plan.Next)
	}
}

func TestPlanMoveLoadsCleanTargetWindow(t *testing.T) {
	plan := PlanMove(MoveRequest{
		FileSize:      10_000,
		Current:       Range{Start: 0, End: 2_000},
		TargetOffset:  7_500,
		ViewportBytes: 400,
		BudgetBytes:   2_000,
	})
	if plan.Action != SlideLoad {
		t.Fatalf("Action = %v, want SlideLoad", plan.Action)
	}
	if plan.Next.End-plan.Next.Start > 2_000 {
		t.Fatalf("Next = %+v exceeds budget", plan.Next)
	}
	if !plan.Next.Contains(plan.Target) {
		t.Fatalf("Next = %+v does not contain Target %+v", plan.Next, plan.Target)
	}
}

func TestPlanMovePromptsWhenDirtyOrStagedWouldLeaveWindow(t *testing.T) {
	for _, tt := range []struct {
		name          string
		dirty         bool
		hasStagedEdit bool
	}{
		{name: "dirty", dirty: true},
		{name: "staged", hasStagedEdit: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanMove(MoveRequest{
				FileSize:       10_000,
				Current:        Range{Start: 0, End: 2_000},
				TargetOffset:   7_500,
				ViewportBytes:  400,
				BudgetBytes:    2_000,
				Dirty:          tt.dirty,
				HasStagedEdits: tt.hasStagedEdit,
			})
			if plan.Action != SlidePromptSave {
				t.Fatalf("Action = %v, want SlidePromptSave", plan.Action)
			}
			if !plan.Next.Contains(plan.Target) {
				t.Fatalf("Next = %+v does not contain Target %+v", plan.Next, plan.Target)
			}
			for _, want := range []string{"Stage local slice edits", "save/discard staged output", "editable-window budget"} {
				if !strings.Contains(plan.Message, want) {
					t.Fatalf("Message = %q, missing %q", plan.Message, want)
				}
			}
		})
	}
}

func TestPlanMoveRequiresLargerBudgetForOversizedViewport(t *testing.T) {
	plan := PlanMove(MoveRequest{
		FileSize:      10_000,
		Current:       Range{Start: 0, End: 2_000},
		TargetOffset:  100,
		ViewportBytes: 2_500,
		BudgetBytes:   2_000,
	})
	if plan.Action != SlideIncreaseLimit {
		t.Fatalf("Action = %v, want SlideIncreaseLimit", plan.Action)
	}
}

func TestPlanMoveClampsTargetNearEnd(t *testing.T) {
	plan := PlanMove(MoveRequest{
		FileSize:      10_000,
		Current:       Range{Start: 0, End: 2_000},
		TargetOffset:  9_700,
		ViewportBytes: 200,
		BudgetBytes:   2_000,
	})
	want := Range{Start: 8_000, End: 10_000}
	if plan.Next != want {
		t.Fatalf("Next = %+v, want %+v", plan.Next, want)
	}
}
