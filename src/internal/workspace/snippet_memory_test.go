package workspace

import (
	"strings"
	"testing"
	"unsafe"
)

func TestClipRunesPreservesBoundariesWithoutRetainingSource(t *testing.T) {
	backing := strings.Repeat("x", 1<<20) + "a\u20ac\U0001f600z"
	input := backing[1<<20:]
	for _, test := range []struct {
		limit int
		want  string
	}{{0, ""}, {1, "a"}, {2, "a\u20ac"}, {3, "a\u20ac\U0001f600"}, {4, input}, {400, input}} {
		got := clipRunes(input, test.limit)
		if got != test.want {
			t.Fatalf("limit=%d: got %q, want %q", test.limit, got, test.want)
		}
		// A short result must own its storage; otherwise a result list can pin
		// gigabytes of source strings despite its small serialized response.
		if len(got) != 0 && unsafe.StringData(got) == unsafe.StringData(input) {
			t.Fatalf("limit=%d: snippet retained the source backing string", test.limit)
		}
	}
}

func BenchmarkClipRunesGiantLine(b *testing.B) {
	line := strings.Repeat("a", 1<<20)
	b.ReportAllocs()
	for b.Loop() {
		if len(clipRunes(line, 400)) != 400 {
			b.Fatal("incorrect snippet length")
		}
	}
}
