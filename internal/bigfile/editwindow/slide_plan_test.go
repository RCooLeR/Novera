package editwindow

import (
	"strings"
	"testing"
)

func TestPlanAdjacentSlideStaysInsideSlice(t *testing.T) {
	plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 2_000, End: 4_000},
		FileSize:    10_000,
		BudgetBytes: 2_000,
		CursorRow:   1,
		TotalRows:   3,
	})
	if plan.Action != SlideStay {
		t.Fatalf("Action = %v, want SlideStay", plan.Action)
	}
}

func TestPlanAdjacentSlideLoadsPreviousCleanSliceAtTop(t *testing.T) {
	plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 4_000, End: 6_000},
		FileSize:    10_000,
		BudgetBytes: 2_000,
		CursorRow:   0,
		TotalRows:   4,
	})
	if plan.Action != SlideLoad {
		t.Fatalf("Action = %v, want SlideLoad", plan.Action)
	}
	if plan.Direction != SlidePrevious {
		t.Fatalf("Direction = %v, want SlidePrevious", plan.Direction)
	}
	if !plan.Next.Contains(Range{Start: 3_999, End: 4_000}) {
		t.Fatalf("Next = %+v, want previous boundary byte covered", plan.Next)
	}
}

func TestPlanAdjacentSlideLoadsNextCleanSliceAtBottom(t *testing.T) {
	plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 4_000, End: 6_000},
		FileSize:    10_000,
		BudgetBytes: 2_000,
		CursorRow:   3,
		TotalRows:   4,
	})
	if plan.Action != SlideLoad {
		t.Fatalf("Action = %v, want SlideLoad", plan.Action)
	}
	if plan.Direction != SlideNext {
		t.Fatalf("Direction = %v, want SlideNext", plan.Direction)
	}
	if !plan.Next.Contains(Range{Start: 6_000, End: 6_001}) {
		t.Fatalf("Next = %+v, want next boundary byte covered", plan.Next)
	}
}

func TestPlanAdjacentSlidePromptsWhenDirtyOrStaged(t *testing.T) {
	for _, tt := range []struct {
		name          string
		dirty         bool
		hasStagedEdit bool
		want          []string
		notWant       []string
	}{
		{
			name:    "dirty",
			dirty:   true,
			want:    []string{"Stage or discard local slice edits", "adjacent slice", "editable-window budget"},
			notWant: []string{"staged output"},
		},
		{
			name:          "staged",
			hasStagedEdit: true,
			want:          []string{"Save or discard staged output", "adjacent slice", "editable-window budget"},
			notWant:       []string{"local slice edits"},
		},
		{
			name:          "dirty and staged",
			dirty:         true,
			hasStagedEdit: true,
			want:          []string{"Stage or discard local slice edits", "save or discard staged output", "adjacent slice", "editable-window budget"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanAdjacentSlide(SlideRequest{
				Current:        Range{Start: 0, End: 2_000},
				FileSize:       10_000,
				BudgetBytes:    2_000,
				CursorRow:      2,
				TotalRows:      3,
				Dirty:          tt.dirty,
				HasStagedEdits: tt.hasStagedEdit,
			})
			if plan.Action != SlidePromptSave {
				t.Fatalf("Action = %v, want SlidePromptSave", plan.Action)
			}
			if plan.Direction != SlideNext {
				t.Fatalf("Direction = %v, want SlideNext", plan.Direction)
			}
			for _, want := range tt.want {
				if !strings.Contains(plan.Message, want) {
					t.Fatalf("Message = %q, missing %q", plan.Message, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(plan.Message, notWant) {
					t.Fatalf("Message = %q, should not contain %q", plan.Message, notWant)
				}
			}
		})
	}
}

func TestPlanAdjacentSlideUsesExplicitScrollIntent(t *testing.T) {
	for _, tt := range []struct {
		name      string
		trigger   SlideTrigger
		direction SlideDirection
		contains  Range
	}{
		{
			name:      "scroll previous",
			trigger:   SlideByScrollPrevious,
			direction: SlidePrevious,
			contains:  Range{Start: 3_999, End: 4_000},
		},
		{
			name:      "scroll next",
			trigger:   SlideByScrollNext,
			direction: SlideNext,
			contains:  Range{Start: 6_000, End: 6_001},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanAdjacentSlide(SlideRequest{
				Current:     Range{Start: 4_000, End: 6_000},
				FileSize:    10_000,
				BudgetBytes: 2_000,
				CursorRow:   1,
				TotalRows:   4,
				Trigger:     tt.trigger,
			})
			if plan.Action != SlideLoad {
				t.Fatalf("Action = %v, want SlideLoad", plan.Action)
			}
			if plan.Direction != tt.direction {
				t.Fatalf("Direction = %v, want %v", plan.Direction, tt.direction)
			}
			if !plan.Next.Contains(tt.contains) {
				t.Fatalf("Next = %+v, want boundary byte %+v covered", plan.Next, tt.contains)
			}
		})
	}
}

func TestPlanAdjacentSlidePromptsForDirtyScrollIntentAwayFromSlice(t *testing.T) {
	plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 4_000, End: 6_000},
		FileSize:    10_000,
		BudgetBytes: 2_000,
		CursorRow:   1,
		TotalRows:   4,
		Dirty:       true,
		Trigger:     SlideByScrollNext,
	})
	if plan.Action != SlidePromptSave {
		t.Fatalf("Action = %v, want SlidePromptSave", plan.Action)
	}
	if plan.Direction != SlideNext {
		t.Fatalf("Direction = %v, want SlideNext", plan.Direction)
	}
}

func TestPlanAdjacentSlideScrollIntentRespectsFileBoundaries(t *testing.T) {
	if plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 0, End: 2_000},
		FileSize:    10_000,
		BudgetBytes: 2_000,
		CursorRow:   3,
		TotalRows:   4,
		Trigger:     SlideByScrollPrevious,
	}); plan.Action != SlideStay || !containsAll(plan.Message, []string{"first editable slice", "cannot move earlier", "search", "Go To", "slice navigation"}) {
		t.Fatalf("previous boundary plan = %#v, want SlideStay with first-slice message", plan)
	}
	if plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 8_000, End: 10_000},
		FileSize:    10_000,
		BudgetBytes: 2_000,
		CursorRow:   0,
		TotalRows:   4,
		Trigger:     SlideByScrollNext,
	}); plan.Action != SlideStay || !containsAll(plan.Message, []string{"final editable slice", "cannot move later", "search", "Go To", "slice navigation"}) {
		t.Fatalf("next boundary plan = %#v, want SlideStay with final-slice message", plan)
	}
}

func TestPlanAdjacentSlideIgnoresWholeFileCursorSingleLineAndSuppressedRuns(t *testing.T) {
	if plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 0, End: 100},
		FileSize:    100,
		BudgetBytes: 200,
		CursorRow:   0,
		TotalRows:   2,
	}); plan.Action != SlideStay {
		t.Fatalf("whole-file Action = %v, want SlideStay", plan.Action)
	}
	if plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 0, End: 2_000},
		FileSize:    10_000,
		BudgetBytes: 2_000,
		CursorRow:   0,
		TotalRows:   1,
	}); plan.Action != SlideStay {
		t.Fatalf("cursor single-line Action = %v, want SlideStay", plan.Action)
	}
	if plan := PlanAdjacentSlide(SlideRequest{
		Current:         Range{Start: 0, End: 2_000},
		FileSize:        10_000,
		BudgetBytes:     2_000,
		CursorRow:       1,
		TotalRows:       2,
		SuppressNextRun: true,
	}); plan.Action != SlideStay {
		t.Fatalf("suppressed Action = %v, want SlideStay", plan.Action)
	}
}

func TestPlanAdjacentSlideAllowsExplicitScrollOnSingleLineSlice(t *testing.T) {
	plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 2_000, End: 4_000},
		FileSize:    10_000,
		BudgetBytes: 2_000,
		CursorRow:   0,
		TotalRows:   1,
		Trigger:     SlideByScrollNext,
	})
	if plan.Action != SlideLoad {
		t.Fatalf("Action = %v, want SlideLoad", plan.Action)
	}
	if plan.Direction != SlideNext {
		t.Fatalf("Direction = %v, want SlideNext", plan.Direction)
	}
	if !plan.Next.Contains(Range{Start: 4_000, End: 4_001}) {
		t.Fatalf("Next = %+v, want next boundary byte covered", plan.Next)
	}
}

func TestPlanAdjacentSlidePromptsForDirtyExplicitScrollOnSingleLineSlice(t *testing.T) {
	plan := PlanAdjacentSlide(SlideRequest{
		Current:     Range{Start: 2_000, End: 4_000},
		FileSize:    10_000,
		BudgetBytes: 2_000,
		CursorRow:   0,
		TotalRows:   1,
		Dirty:       true,
		Trigger:     SlideByScrollPrevious,
	})
	if plan.Action != SlidePromptSave {
		t.Fatalf("Action = %v, want SlidePromptSave", plan.Action)
	}
	if plan.Direction != SlidePrevious {
		t.Fatalf("Direction = %v, want SlidePrevious", plan.Direction)
	}
}

func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
