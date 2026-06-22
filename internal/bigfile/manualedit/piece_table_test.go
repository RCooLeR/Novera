package manualedit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

type memReaderAtSize struct {
	data []byte
}

func (r memReaderAtSize) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r memReaderAtSize) Size() int64 {
	return int64(len(r.data))
}

type nopSyncBuffer struct {
	bytes.Buffer
}

func (b *nopSyncBuffer) Sync() error {
	return nil
}

// shrunkReaderAtSize reports a larger Size() than it actually serves, simulating
// a source that was truncated/replaced after the piece table was built.
type shrunkReaderAtSize struct {
	data         []byte
	reportedSize int64
}

func (r shrunkReaderAtSize) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r shrunkReaderAtSize) Size() int64 { return r.reportedSize }

// WriteTo must fail loudly (not silently truncate) when an original piece reads
// short because the source shrank since staging.
func TestPieceTableWriteToFailsOnShortSource(t *testing.T) {
	full := []byte("alpha bravo charlie delta echo foxtrot")
	pt := NewPieceTable(int64(len(full)))
	// No edits: the whole file is one original piece of length len(full).
	src := shrunkReaderAtSize{data: full[:10], reportedSize: int64(len(full))}
	var out nopSyncBuffer
	_, err := pt.WriteTo(context.Background(), src, &out, WriteOptions{})
	if !errors.Is(err, ErrSourceModifiedDuringOperation) {
		t.Fatalf("err = %v, want ErrSourceModifiedDuringOperation", err)
	}
}

func TestPieceTableReplaceWritesUpdatedBytes(t *testing.T) {
	src := []byte("alpha bravo charlie")
	pt := NewPieceTable(int64(len(src)))
	if err := pt.Replace(6, 11, []byte("delta")); err != nil {
		t.Fatal(err)
	}

	var out nopSyncBuffer
	written, err := pt.WriteTo(context.Background(), memReaderAtSize{data: src}, &out, WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len("alpha delta charlie")) {
		t.Fatalf("written = %d", written)
	}
	if out.String() != "alpha delta charlie" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestPieceTableInsertAtEnd(t *testing.T) {
	src := []byte("hello")
	pt := NewPieceTable(int64(len(src)))
	if err := pt.Replace(5, 5, []byte(" world")); err != nil {
		t.Fatal(err)
	}

	var out nopSyncBuffer
	if _, err := pt.WriteTo(context.Background(), memReaderAtSize{data: src}, &out, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello world" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestPieceTableDeleteRange(t *testing.T) {
	src := []byte("one two three")
	pt := NewPieceTable(int64(len(src)))
	if err := pt.Replace(3, 8, nil); err != nil {
		t.Fatal(err)
	}

	var out nopSyncBuffer
	if _, err := pt.WriteTo(context.Background(), memReaderAtSize{data: src}, &out, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "onethree" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestPieceTableRejectsOversizedInsertedText(t *testing.T) {
	pt := NewPieceTable(3)
	pt.SetMaxInsertedBytes(4)
	if err := pt.Replace(1, 1, []byte("abcdef")); !errors.Is(err, ErrInsertedTextTooLarge) {
		t.Fatalf("err = %v, want ErrInsertedTextTooLarge", err)
	}
}
