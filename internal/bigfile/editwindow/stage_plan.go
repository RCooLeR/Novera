package editwindow

type StageAction int

const (
	StageReject StageAction = iota
	StageNoChange
	StagePatch
	StageUpdateExisting
	StageConflict
)

type StageRequest struct {
	Window                   Range
	FileSize                 int64
	BudgetBytes              int64
	ReplacementBytes         int64
	Changed                  bool
	ExistingModifiedRanges   []Range
	LastStagedSourceRange    Range
	HasLastStagedSourceRange bool
}

type StagePlan struct {
	Action      StageAction
	SourceRange Range
	Message     string
}

func PlanStage(req StageRequest) StagePlan {
	if !req.Window.Valid() || req.Window.Start > req.FileSize || req.Window.End > req.FileSize {
		return StagePlan{Action: StageReject, Message: "editable slice range is outside the source file"}
	}
	if coversWholeFile(req.Window, req.FileSize) {
		return StagePlan{Action: StageReject, Message: "whole-file buffers use save-copy, not staged slice replacement"}
	}
	if !req.Changed {
		return StagePlan{Action: StageNoChange, SourceRange: req.Window, Message: "editable slice has no changes to stage"}
	}
	if req.BudgetBytes <= 0 || req.ReplacementBytes > req.BudgetBytes {
		return StagePlan{Action: StageReject, SourceRange: req.Window, Message: "encoded replacement exceeds the editable-window budget"}
	}
	for _, existing := range req.ExistingModifiedRanges {
		if rangesOverlap(req.Window, existing) {
			if req.HasLastStagedSourceRange && rangesEqual(req.Window, existing) && rangesEqual(req.Window, req.LastStagedSourceRange) {
				return StagePlan{
					Action:      StageUpdateExisting,
					SourceRange: req.Window,
					Message:     "update existing editable-slice replacement",
				}
			}
			return StagePlan{
				Action:      StageConflict,
				SourceRange: req.Window,
				Message:     "editable slice overlaps an existing staged source range; save or discard staged edits before replacing this slice again",
			}
		}
	}
	return StagePlan{Action: StagePatch, SourceRange: req.Window, Message: "stage editable-slice replacement"}
}

func rangesEqual(a, b Range) bool {
	return a.Start == b.Start && a.End == b.End
}

func rangesOverlap(a, b Range) bool {
	if !a.Valid() || !b.Valid() {
		return false
	}
	aEnd := a.End
	if aEnd <= a.Start {
		aEnd = a.Start + 1
	}
	bEnd := b.End
	if bEnd <= b.Start {
		bEnd = b.Start + 1
	}
	return a.Start < bEnd && b.Start < aEnd
}
