package manualedit

import "testing"

func TestRangeOverlaps(t *testing.T) {
	ranges := []Range{
		{Start: 10, End: 20},
		{Start: 40, End: 40},
	}

	cases := []struct {
		name  string
		start int64
		end   int64
		want  bool
	}{
		{name: "before", start: 0, end: 9, want: false},
		{name: "intersects body", start: 15, end: 18, want: true},
		{name: "touches boundary only", start: 20, end: 25, want: false},
		{name: "crosses boundary", start: 19, end: 25, want: true},
		{name: "zero width insertion point", start: 40, end: 40, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RangeOverlaps(tc.start, tc.end, ranges)
			if got != tc.want {
				t.Fatalf("RangeOverlaps(%d, %d) = %v, want %v", tc.start, tc.end, got, tc.want)
			}
		})
	}
}

func TestFindNextRangeIndex(t *testing.T) {
	ranges := []Range{
		{Start: 10, End: 15},
		{Start: 30, End: 35},
		{Start: 50, End: 55},
	}

	if got := FindNextRangeIndex(ranges, 10, true); got != 1 {
		t.Fatalf("forward next = %d, want 1", got)
	}
	if got := FindNextRangeIndex(ranges, 55, true); got != 0 {
		t.Fatalf("forward wrap = %d, want 0", got)
	}
	if got := FindNextRangeIndex(ranges, 30, false); got != 0 {
		t.Fatalf("backward previous = %d, want 0", got)
	}
	if got := FindNextRangeIndex(ranges, 5, false); got != 2 {
		t.Fatalf("backward wrap = %d, want 2", got)
	}
	if got := FindNextRangeIndex(nil, 10, true); got != -1 {
		t.Fatalf("empty = %d, want -1", got)
	}
}
