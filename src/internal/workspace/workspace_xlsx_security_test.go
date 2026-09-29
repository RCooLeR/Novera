package workspace

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// Excelize 2.11 fixes the in-memory lookup reported by GO-2026-6452 but its
// temporary-file lookup still panics for negative indices. Exercise both paths
// through the application's iterator, including resource cleanup after errors.
func TestXLSXSharedStringIndicesAreValidated(t *testing.T) {
	for _, tc := range []struct {
		name      string
		row       int
		index     int
		spilled   bool
		wantError bool
	}{
		{name: "valid", row: 2, index: 1},
		{name: "negative_header", row: 1, index: -1},
		{name: "negative_data", row: 2, index: -1},
		{name: "valid_spilled", row: 2, index: 1, spilled: true},
		{name: "negative_header_spilled", row: 1, index: -1, spilled: true, wantError: true},
		{name: "negative_data_spilled", row: 2, index: -1, spilled: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Route Excelize's spill files into a dedicated directory and prove
			// that both successful and failed iterators remove them when closed.
			tempDir := t.TempDir()
			t.Setenv("TMPDIR", tempDir)
			t.Setenv("TMP", tempDir)
			t.Setenv("TEMP", tempDir)
			path := workbookWithSharedStringIndex(t, tc.row, tc.index, tc.spilled)
			header, iter, err := openTableIter(path, filepath.Base(path), "")
			if iter != nil {
				t.Cleanup(iter.close)
			}
			var row []string
			var ok bool
			if err == nil {
				row, ok, err = iter.next()
			}
			if iter != nil {
				iter.close()
			}
			entries, readErr := os.ReadDir(tempDir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("workbook temporary files leaked: entries=%v err=%v", entries, readErr)
			}
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "invalid workbook row") {
					t.Fatalf("invalid shared string index %d returned %v", tc.index, err)
				}
				return
			}
			if tc.index < 0 {
				// The dependency treats invalid in-memory indices as empty cells.
				// They must never panic or return data from another shared string.
				if err != nil || (tc.row == 1 && len(header) != 0) || (tc.row == 2 && len(row) != 0) {
					t.Fatalf("invalid in-memory index: header=%v row=%v err=%v", header, row, err)
				}
				return
			}
			if err != nil || !ok || len(header) != 1 || header[0] != "header" || len(row) != 1 || row[0] != "value" {
				t.Fatalf("valid workbook: header=%v row=%v ok=%v err=%v", header, row, ok, err)
			}
		})
	}
}

func workbookWithSharedStringIndex(t *testing.T, row, index int, spilled bool) string {
	t.Helper()
	book := excelize.NewFile()
	defer book.Close()
	if err := book.SetCellStr("Sheet1", "A1", "header"); err != nil {
		t.Fatal(err)
	}
	if err := book.SetCellStr("Sheet1", "A2", "value"); err != nil {
		t.Fatal(err)
	}
	source, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(source.Bytes()), int64(source.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for _, file := range zr.File {
		if file.Name != "xl/worksheets/sheet1.xml" && !(spilled && file.Name == "xl/sharedStrings.xml") {
			if err := zw.Copy(file); err != nil {
				t.Fatal(err)
			}
			continue
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if file.Name == "xl/worksheets/sheet1.xml" {
			before := fmt.Sprintf(`<c r="A%d" t="s"><v>%d</v></c>`, row, row-1)
			after := fmt.Sprintf(`<c r="A%d" t="s"><v>%d</v></c>`, row, index)
			if !bytes.Contains(data, []byte(before)) {
				t.Fatalf("fixture does not contain expected shared-string cell %q", before)
			}
			data = bytes.Replace(data, []byte(before), []byte(after), 1)
		} else {
			// Legal trailing XML whitespace forces the real >16MiB spill path
			// without creating large cells or changing the shared string table.
			data = append(data, bytes.Repeat([]byte(" "), maxXlsxXMLMemoryBytes)...)
		}
		w, err := zw.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "shared-strings.xlsx")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
