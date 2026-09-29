package editwindow

type OpenMode int

const (
	OpenModeMetadataOnly OpenMode = iota
	OpenModeFullBufferAuto
	OpenModeFullBufferOnDemand
	OpenModeSliceAuto
	OpenModeBudgetTooSmall
)

type OpenRequest struct {
	FileSize            int64
	EditableBudgetBytes int64
	SmallAutoLoadBytes  int64
	Binary              bool
}

type OpenPlan struct {
	Mode  OpenMode
	Slice Range
}

func PlanOpen(req OpenRequest) OpenPlan {
	if req.Binary {
		return OpenPlan{Mode: OpenModeMetadataOnly}
	}
	if req.EditableBudgetBytes <= 0 {
		return OpenPlan{Mode: OpenModeBudgetTooSmall}
	}
	if req.FileSize <= req.EditableBudgetBytes {
		if req.FileSize <= maxInt64(req.SmallAutoLoadBytes, 0) {
			return OpenPlan{Mode: OpenModeFullBufferAuto}
		}
		return OpenPlan{Mode: OpenModeFullBufferOnDemand}
	}
	first, ok := InitialSlice(req.FileSize, req.EditableBudgetBytes)
	if !ok {
		return OpenPlan{Mode: OpenModeBudgetTooSmall}
	}
	return OpenPlan{Mode: OpenModeSliceAuto, Slice: first}
}
