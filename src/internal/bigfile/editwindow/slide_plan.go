package editwindow

type SlideAction int

const (
	SlideStay SlideAction = iota
	SlideLoad
	SlidePromptSave
	SlideIncreaseLimit
)

type SlideDirection int

const (
	SlideNone SlideDirection = iota
	SlidePrevious
	SlideNext
)

type SlideTrigger int

const (
	// SlideByCursorBoundary uses the caret row to decide whether the active
	// editable slice can move. This is the normal keyboard/caret path.
	SlideByCursorBoundary SlideTrigger = iota
	// SlideByScrollPrevious models an explicit scroll/wheel attempt past the
	// start of the active editable slice.
	SlideByScrollPrevious
	// SlideByScrollNext models an explicit scroll/wheel attempt past the end
	// of the active editable slice.
	SlideByScrollNext
)

type SlideRequest struct {
	Current         Range
	FileSize        int64
	BudgetBytes     int64
	CursorRow       int
	TotalRows       int
	Dirty           bool
	HasStagedEdits  bool
	SuppressNextRun bool
	Trigger         SlideTrigger
}

type SlidePlan struct {
	Action    SlideAction
	Direction SlideDirection
	Next      Range
	Message   string
}

func PlanAdjacentSlide(req SlideRequest) SlidePlan {
	if req.SuppressNextRun || !req.Current.Valid() || coversWholeFile(req.Current, req.FileSize) {
		return SlidePlan{Action: SlideStay}
	}
	if req.TotalRows < 2 && req.Trigger == SlideByCursorBoundary {
		return SlidePlan{Action: SlideStay}
	}

	var direction SlideDirection
	var next Range
	var ok bool
	switch {
	case req.Trigger == SlideByScrollPrevious:
		if req.Current.Start <= 0 {
			return SlidePlan{Action: SlideStay, Message: "Already at the first editable slice. The loaded editor window cannot move earlier; use search, Go To, or slice navigation for another location."}
		}
		direction = SlidePrevious
		next, ok = PreviousSlice(req.Current, req.FileSize, req.BudgetBytes)
	case req.Trigger == SlideByScrollNext:
		if req.Current.End >= req.FileSize {
			return SlidePlan{Action: SlideStay, Message: "Already at the final editable slice. The loaded editor window cannot move later; use search, Go To, or slice navigation for another location."}
		}
		direction = SlideNext
		next, ok = NextSlice(req.Current, req.FileSize, req.BudgetBytes)
	case req.CursorRow <= 0 && req.Current.Start > 0:
		direction = SlidePrevious
		next, ok = PreviousSlice(req.Current, req.FileSize, req.BudgetBytes)
	case req.CursorRow >= req.TotalRows-1 && req.Current.End < req.FileSize:
		direction = SlideNext
		next, ok = NextSlice(req.Current, req.FileSize, req.BudgetBytes)
	default:
		return SlidePlan{Action: SlideStay}
	}

	if !ok {
		return SlidePlan{
			Action:    SlideIncreaseLimit,
			Direction: direction,
			Message:   "The adjacent editable slice is larger than the editable-window budget; increase the limit before loading it into the normal editor.",
		}
	}
	if req.Dirty || req.HasStagedEdits {
		return SlidePlan{
			Action:    SlidePromptSave,
			Direction: direction,
			Next:      next,
			Message:   slidePendingWorkMessage(req.Dirty, req.HasStagedEdits),
		}
	}
	return SlidePlan{
		Action:    SlideLoad,
		Direction: direction,
		Next:      next,
		Message:   "Load the adjacent editable-window slice.",
	}
}

func slidePendingWorkMessage(dirty bool, hasStagedEdits bool) string {
	switch {
	case dirty && hasStagedEdits:
		return "Stage or discard local slice edits, then save or discard staged output before loading the adjacent slice, or increase the editable-window budget."
	case dirty:
		return "Stage or discard local slice edits before loading the adjacent slice, or increase the editable-window budget."
	case hasStagedEdits:
		return "Save or discard staged output before loading the adjacent slice, or increase the editable-window budget."
	default:
		return "Resolve pending editable-slice work before loading the adjacent slice, or increase the editable-window budget."
	}
}

func coversWholeFile(r Range, fileSize int64) bool {
	return r.Start <= 0 && r.End >= maxInt64(fileSize, 0)
}
