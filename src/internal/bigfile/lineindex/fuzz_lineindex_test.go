package lineindex

import (
	"bytes"
	"context"
	"testing"
)

var fuzzLineIndexEncodings = []string{"UTF-8", "UTF-16LE", "UTF-16BE"}

func FuzzBuildWithEncodingNewlineSoup(f *testing.F) {
	f.Add([]byte("alpha\nbeta\ngamma\n"), uint8(0), int64(2))
	f.Add([]byte("alpha\r\nbeta\rgamma\n"), uint8(0), int64(3))
	f.Add([]byte{0x61, 0x00, 0x0A, 0x00, 0x62, 0x00}, uint8(1), int64(2))
	f.Add([]byte{0x00, 0x61, 0x00, 0x0A, 0x00, 0x62}, uint8(2), int64(2))

	f.Fuzz(func(t *testing.T, data []byte, encodingSeed uint8, everyLines int64) {
		if len(data) > 8192 {
			data = data[:8192]
		}
		if everyLines < 1 {
			everyLines = 1
		}
		everyLines = (everyLines % 16) + 1
		encodingName := fuzzLineIndexEncodings[int(encodingSeed)%len(fuzzLineIndexEncodings)]

		idx := New(everyLines)
		if err := idx.BuildWithEncoding(context.Background(), bytes.NewReader(data), encodingName); err != nil {
			t.Fatal(err)
		}
		if !idx.Done() {
			t.Fatal("index did not mark done")
		}
		progress := idx.Progress()
		if progress.Bytes != int64(len(data)) {
			t.Fatalf("progress bytes = %d, want %d", progress.Bytes, len(data))
		}
		var last Entry
		for i, entry := range idx.Entries() {
			if entry.Line < 1 || entry.Offset < 0 || entry.Offset > int64(len(data)) {
				t.Fatalf("entry out of bounds: %+v size=%d", entry, len(data))
			}
			if i > 0 && (entry.Line < last.Line || entry.Offset < last.Offset) {
				t.Fatalf("entries not sorted: previous=%+v current=%+v", last, entry)
			}
			last = entry
		}
	})
}
