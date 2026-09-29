package editwindow

// Range is a byte range that can be loaded into Novera's bounded editable
// window without treating the entire source file as an in-memory document.
type Range struct {
	Start int64
	End   int64
}

func (r Range) Valid() bool {
	return r.Start >= 0 && r.End >= r.Start
}

func (r Range) Contains(other Range) bool {
	return r.Valid() && other.Valid() && other.Start >= r.Start && other.End <= r.End
}

func TargetRange(fileSize, targetOffset, viewportBytes int64) Range {
	if fileSize < 0 {
		fileSize = 0
	}
	if viewportBytes < 1 {
		viewportBytes = 1
	}
	if targetOffset < 0 {
		targetOffset = 0
	}
	if targetOffset > fileSize {
		targetOffset = fileSize
	}
	end := targetOffset + viewportBytes
	if end < targetOffset || end > fileSize {
		end = fileSize
	}
	return Range{Start: targetOffset, End: end}
}

func Around(fileSize, targetOffset, viewportBytes, budgetBytes int64) (Range, bool) {
	target := TargetRange(fileSize, targetOffset, viewportBytes)
	needed := target.End - target.Start
	if budgetBytes <= 0 || needed > budgetBytes {
		return Range{}, false
	}
	if fileSize <= budgetBytes {
		return Range{Start: 0, End: maxInt64(fileSize, 0)}, true
	}
	start := target.Start - budgetBytes/2
	if target.End > start+budgetBytes {
		start = target.End - budgetBytes
	}
	maxStart := fileSize - budgetBytes
	if start < 0 {
		start = 0
	}
	if start > maxStart {
		start = maxStart
	}
	return Range{Start: start, End: start + budgetBytes}, true
}

func InitialSlice(fileSize, budgetBytes int64) (Range, bool) {
	return Around(fileSize, 0, 1, budgetBytes)
}

func NextSlice(current Range, fileSize, budgetBytes int64) (Range, bool) {
	if !current.Valid() || coversWholeFile(current, fileSize) || current.End >= maxInt64(fileSize, 0) {
		return Range{}, false
	}
	return Around(fileSize, current.End, 1, budgetBytes)
}

func PreviousSlice(current Range, fileSize, budgetBytes int64) (Range, bool) {
	if !current.Valid() || coversWholeFile(current, fileSize) || current.Start <= 0 {
		return Range{}, false
	}
	return Around(fileSize, current.Start-1, 1, budgetBytes)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
