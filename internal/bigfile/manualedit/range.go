package manualedit

func RangeOverlaps(start int64, end int64, ranges []Range) bool {
	if end <= start {
		end = start + 1
	}
	for _, r := range ranges {
		rangeEnd := r.End
		if rangeEnd <= r.Start {
			rangeEnd = r.Start + 1
		}
		if start < rangeEnd && r.Start < end {
			return true
		}
	}
	return false
}

func FindNextRangeIndex(ranges []Range, current int64, forward bool) int {
	if len(ranges) == 0 {
		return -1
	}
	if forward {
		for i, r := range ranges {
			if r.Start > current {
				return i
			}
		}
		return 0
	}
	for i := len(ranges) - 1; i >= 0; i-- {
		if ranges[i].Start < current {
			return i
		}
	}
	return len(ranges) - 1
}
