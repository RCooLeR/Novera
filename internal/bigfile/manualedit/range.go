package manualedit

import "slices"

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
	for i, candidate := range slices.Backward(ranges) {
		if candidate.Start < current {
			return i
		}
	}
	return len(ranges) - 1
}
