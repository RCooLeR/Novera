package workspace

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenWorkbookWithLimitsRejectsCompressedAndExpandedBudgets(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("[Content_Types].xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(bytes.Repeat([]byte("x"), 1024)); err != nil {
		t.Fatal(err)
	}
	entry, err = zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(bytes.Repeat([]byte("y"), 256)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bomb.xlsx")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := openWorkbookWithLimits(path, int64(archive.Len()-1), 2048, 128); !errors.Is(err, ErrRawTooLarge) {
		t.Fatalf("compressed budget error = %v, want ErrRawTooLarge", err)
	}
	f, err := openWorkbookWithLimits(path, int64(archive.Len()+1), 512, 128)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("expanded workbook exceeding aggregate unzip budget was accepted")
	}
	if errors.Is(err, ErrRawTooLarge) {
		t.Fatalf("expanded budget test hit the compressed ceiling instead: %v", err)
	}
	if err := validateWorkbookArchive(archive.Bytes(), 2048, 2); err != nil {
		t.Fatalf("bounded archive preflight rejected a valid archive: %v", err)
	}
	if err := validateWorkbookArchive(archive.Bytes(), 2048, 1); err == nil {
		t.Fatal("archive entry-count limit was not enforced")
	}

	// Forge the first central-directory uncompressed size to one byte while its
	// deflate stream still expands to 1024. Preflight must measure the stream and
	// reject the metadata mismatch instead of trusting the smaller header value.
	forged := append([]byte(nil), archive.Bytes()...)
	central := bytes.Index(forged, []byte{'P', 'K', 1, 2})
	if central < 0 || central+28 > len(forged) {
		t.Fatal("test archive has no central-directory entry")
	}
	binary.LittleEndian.PutUint32(forged[central+24:central+28], 1)
	if err := validateWorkbookArchive(forged, 2048, 2); err == nil {
		t.Fatal("archive with forged uncompressed size was accepted")
	}
}
