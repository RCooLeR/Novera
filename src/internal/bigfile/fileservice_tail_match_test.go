package bigfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/encodingx"
)

func openTailFixture(t *testing.T, name string, data []byte) (*FileService, FileMeta, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.ServiceShutdown(); err != nil {
			t.Errorf("ServiceShutdown: %v", err)
		}
	})
	return service, meta, path
}

func TestGetTailWindowReturnsFinalNonEmptyBoundedWindow(t *testing.T) {
	var content strings.Builder
	for i := 1; i <= 200; i++ {
		content.WriteString("record-")
		content.WriteString(strings.Repeat("x", i%7))
		content.WriteByte('\n')
	}
	content.WriteString("final-record")
	service, meta, _ := openTailFixture(t, "tail.log", []byte(content.String()))

	window, err := service.GetTailWindow(meta.FileID, 64)
	if err != nil {
		t.Fatal(err)
	}
	if window.Text == "" || !strings.Contains(window.Text, "final-record") {
		t.Fatalf("tail window text = %q, want final record", window.Text)
	}
	if !window.AtEOF || window.StartByte >= meta.Size || window.NextByte != meta.Size {
		t.Fatalf("tail metadata = %+v, want non-empty window ending at EOF", window)
	}
	if window.NextByte-window.StartByte > 64 {
		t.Fatalf("tail consumed %d bytes, budget 64", window.NextByte-window.StartByte)
	}
}

func TestGetTailWindowHandlesEmptyAndUTF16Sources(t *testing.T) {
	emptyService, emptyMeta, _ := openTailFixture(t, "empty.log", nil)
	empty, err := emptyService.GetTailWindow(emptyMeta.FileID, 64)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Text != "" || !empty.AtBOF || !empty.AtEOF || len(empty.LineOffsets) != 0 {
		t.Fatalf("empty tail window = %+v", empty)
	}

	encoded, err := encodingx.EncodeString("UTF-16LE", "first\r\nsecond\r\nfinal")
	if err != nil {
		t.Fatal(err)
	}
	raw := append(encodingx.BOMBytes("UTF-16LE"), encoded...)
	utf16Service, utf16Meta, _ := openTailFixture(t, "tail-utf16.log", raw)
	window, err := utf16Service.GetTailWindow(utf16Meta.FileID, 48)
	if err != nil {
		t.Fatal(err)
	}
	if !window.AtEOF || !strings.Contains(window.Text, "final") {
		t.Fatalf("UTF-16 tail = %+v", window)
	}
}

func TestGetTailWindowRejectsUnboundedBudgets(t *testing.T) {
	service, meta, _ := openTailFixture(t, "tail-budget.log", []byte("line\n"))
	for _, budget := range []int{-1, defaultWindowBytes + 1} {
		if _, err := service.GetTailWindow(meta.FileID, budget); err == nil {
			t.Fatalf("GetTailWindow budget %d unexpectedly succeeded", budget)
		}
	}
}

func TestFileStateDetectsGrowthRefreshAndRotationIdentity(t *testing.T) {
	service, meta, path := openTailFixture(t, "follow.log", []byte("one\n"))
	initial, err := service.FileState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if initial.ChangedFromOpen || !initial.SameOpenedFile || initial.Size != meta.Size {
		t.Fatalf("initial state = %+v", initial)
	}
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := service.FileState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if !changed.ChangedFromOpen || !changed.SameOpenedFile || changed.Size <= initial.Size {
		t.Fatalf("grown state = %+v, initial = %+v", changed, initial)
	}
	if _, err := service.RefreshFile(meta.FileID); err != nil {
		t.Fatal(err)
	}
	rebound, err := service.FileState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if rebound.ChangedFromOpen || !rebound.SameOpenedFile {
		t.Fatalf("refreshed state = %+v", rebound)
	}

	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Skipf("filesystem cannot rotate an opened file: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotation, err := service.FileState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if rotation.SameOpenedFile || !rotation.ChangedFromOpen {
		t.Fatalf("rotation state = %+v, want identity mismatch", rotation)
	}
}

func TestGetMatchWindowMapsRawUTF8BytesToJavaScriptUTF16(t *testing.T) {
	const prefix = "pre-a😀"
	const query = "needle"
	content := prefix + query + "-post\n"
	service, meta, _ := openTailFixture(t, "match-utf8.txt", []byte(content))
	match, err := service.GetMatchWindow(meta.FileID, int64(len([]byte(prefix))), len(query), 1024)
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := utf16CodeUnits(prefix)
	if !match.Found || match.From != wantFrom || match.To != wantFrom+len(query) {
		t.Fatalf("UTF-8 match = %+v, want span [%d,%d)", match, wantFrom, wantFrom+len(query))
	}
	if !strings.Contains(match.Window.Text, query) {
		t.Fatalf("match window text = %q", match.Window.Text)
	}

	zero, err := service.GetMatchWindow(meta.FileID, int64(len([]byte(prefix))), 0, 64)
	if err != nil {
		t.Fatal(err)
	}
	if !zero.Found || zero.From != wantFrom || zero.To != wantFrom {
		t.Fatalf("zero-length UTF-8 boundary = %+v, want %d", zero, wantFrom)
	}
}

func TestGetMatchWindowMapsUTF16BOMAndSurrogateCoordinates(t *testing.T) {
	const prefix = "a😀"
	const query = "Z"
	encoded, err := encodingx.EncodeString("UTF-16LE", prefix+query+"\n")
	if err != nil {
		t.Fatal(err)
	}
	data := append(encodingx.BOMBytes("UTF-16LE"), encoded...)
	service, meta, _ := openTailFixture(t, "match-utf16.txt", data)
	prefixBytes, err := encodingx.EncodeString("UTF-16LE", prefix)
	if err != nil {
		t.Fatal(err)
	}
	queryBytes, err := encodingx.EncodeString("UTF-16LE", query)
	if err != nil {
		t.Fatal(err)
	}
	hitOffset := int64(len(encodingx.BOMBytes("UTF-16LE")) + len(prefixBytes))
	match, err := service.GetMatchWindow(meta.FileID, hitOffset, len(queryBytes), 1024)
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := utf16CodeUnits(prefix)
	if !match.Found || match.From != wantFrom || match.To != wantFrom+1 {
		t.Fatalf("UTF-16 match = %+v, want span [%d,%d)", match, wantFrom, wantFrom+1)
	}
}

func TestGetMatchWindowRejectsInvalidHitRanges(t *testing.T) {
	service, meta, _ := openTailFixture(t, "match-range.txt", []byte("abc"))
	cases := []struct {
		offset int64
		length int
		budget int
	}{
		{offset: -1, length: 1, budget: 64},
		{offset: 4, length: 0, budget: 64},
		{offset: 2, length: 2, budget: 64},
		{offset: 0, length: 2, budget: 1},
	}
	for _, tc := range cases {
		if _, err := service.GetMatchWindow(meta.FileID, tc.offset, tc.length, tc.budget); err == nil {
			t.Fatalf("GetMatchWindow(%d,%d,%d) unexpectedly succeeded", tc.offset, tc.length, tc.budget)
		}
	}
}
