package document

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzVisiblePageFromOffsetBounds(f *testing.F) {
	f.Add("alpha\nbeta\ngamma\n", int64(0), uint8(3), uint8(16), uint8(0))
	f.Add("one very long line without a break", int64(8), uint8(2), uint8(4), uint8(3))
	f.Add("crlf\r\nmixed\rlf\n", int64(5), uint8(4), uint8(8), uint8(1))

	f.Fuzz(func(t *testing.T, text string, offset int64, countSeed uint8, maxLineSeed uint8, horizontalSeed uint8) {
		if len(text) > 4096 {
			text = text[:4096]
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "fuzz.txt")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		doc, err := OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		defer doc.Close()

		count := int(countSeed%8) + 1
		maxLineBytes := int(maxLineSeed%64) + 1
		page, err := doc.VisiblePageFromOffset(offset, count, VisibleLineOptions{
			MaxBytes:             256,
			MaxLineBytes:         maxLineBytes,
			LongLineLimitBytes:   maxLineBytes * 4,
			HorizontalByteOffset: int(horizontalSeed % 16),
		})
		if err != nil {
			t.Fatal(err)
		}
		if page.StartOffset < 0 || page.StartOffset > doc.Size() {
			t.Fatalf("StartOffset = %d outside [0,%d]", page.StartOffset, doc.Size())
		}
		if page.NextOffset < page.StartOffset || page.NextOffset > doc.Size() {
			t.Fatalf("NextOffset = %d outside [%d,%d]", page.NextOffset, page.StartOffset, doc.Size())
		}
		if len(page.Lines) > count {
			t.Fatalf("lines = %d, want <= count %d", len(page.Lines), count)
		}
		for _, line := range page.Lines {
			if line.Offset < 0 || line.Offset > doc.Size() || line.DisplayOffset < line.Offset || line.DisplayEndOffset < line.DisplayOffset {
				t.Fatalf("invalid line offsets: %+v size=%d", line, doc.Size())
			}
			if line.DisplayEndOffset > doc.Size() {
				t.Fatalf("line display end = %d > size %d", line.DisplayEndOffset, doc.Size())
			}
		}
	})
}
