package editwindow

type MoveRequest struct {
	Current        Range
	FileSize       int64
	TargetOffset   int64
	ViewportBytes  int64
	BudgetBytes    int64
	Dirty          bool
	HasStagedEdits bool
}

type MovePlan struct {
	Action  SlideAction
	Current Range
	Target  Range
	Next    Range
	Message string
}

// PlanMove decides whether a target viewport can stay inside the active
// editable slice, load another bounded slice, prompt for pending work, or ask
// the user to increase the editable-window budget.
func PlanMove(req MoveRequest) MovePlan {
	target := TargetRange(req.FileSize, req.TargetOffset, req.ViewportBytes)
	plan := MovePlan{
		Action:  SlideStay,
		Current: req.Current,
		Target:  target,
		Message: "Editable window already covers the target viewport.",
	}
	if req.BudgetBytes <= 0 || target.End-target.Start > req.BudgetBytes {
		plan.Action = SlideIncreaseLimit
		plan.Message = "Target viewport is larger than the editable-window budget; increase the limit before loading it into the normal editor."
		return plan
	}
	if req.Current.Contains(target) {
		return plan
	}
	next, ok := Around(req.FileSize, target.Start, target.End-target.Start, req.BudgetBytes)
	if !ok {
		plan.Action = SlideIncreaseLimit
		plan.Message = "Target viewport is larger than the editable-window budget; increase the limit before loading it into the normal editor."
		return plan
	}
	plan.Next = next
	if req.Dirty || req.HasStagedEdits {
		plan.Action = SlidePromptSave
		plan.Message = "Stage local slice edits or save/discard staged output before loading a different editable-window slice, or increase the editable-window budget."
		return plan
	}
	plan.Action = SlideLoad
	plan.Message = "Load the target editable-window slice."
	return plan
}
