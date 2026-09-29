package replace

import (
	"context"
	"io"
	"testing"
)

type memReaderAt struct {
	data []byte
}

func (m memReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m memReaderAt) Size() int64 {
	return int64(len(m.data))
}

func TestPreviewPlain(t *testing.T) {
	r := memReaderAt{data: []byte("alpha hello bravo\ncharlie hello delta")}

	previews, err := PreviewPlain(context.Background(), r, []byte("hello"), []byte("bye"), PreviewOptions{
		ChunkSize:    7,
		MaxHits:      2,
		PreviewBytes: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 2 {
		t.Fatalf("len(previews) = %d, want 2", len(previews))
	}
	if previews[0].Offset != 6 {
		t.Fatalf("first offset = %d, want 6", previews[0].Offset)
	}
	if previews[0].Before != "alpha hello bravo" {
		t.Fatalf("before = %q", previews[0].Before)
	}
	if previews[0].After != "alpha bye bravo" {
		t.Fatalf("after = %q", previews[0].After)
	}
}

func TestPreviewPlainSearchOptions(t *testing.T) {
	r := memReaderAt{data: []byte("cat catalog CAT cat_ cat.")}

	previews, err := PreviewPlain(context.Background(), r, []byte("cat"), []byte("dog"), PreviewOptions{
		ChunkSize:       6,
		MaxHits:         10,
		PreviewBytes:    2,
		CaseInsensitive: true,
		WholeWord:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 3 {
		t.Fatalf("len(previews) = %d, want 3", len(previews))
	}
	if previews[0].After != "dog c" {
		t.Fatalf("first after = %q", previews[0].After)
	}
}
