package workspace

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

// Exercise both shared-string storage paths through the application's iterator.
// The security-patched dependency treats invalid indices as empty cells in both
// paths; neither may panic, expose another string, or leak temporary files.
func TestXLSXSharedStringIndicesAreValidated(t *testing.T) {
	for _, tc := range []struct {
		name    string
		row     int
		index   int
		spilled bool
	}{
		{name: "valid", row: 2, index: 1},
		{name: "negative_header", row: 1, index: -1},
		{name: "negative_data", row: 2, index: -1},
		{name: "past_end_header", row: 1, index: 2},
		{name: "past_end_data", row: 2, index: 2},
		{name: "valid_spilled", row: 2, index: 1, spilled: true},
		{name: "negative_header_spilled", row: 1, index: -1, spilled: true},
		{name: "negative_data_spilled", row: 2, index: -1, spilled: true},
		{name: "past_end_header_spilled", row: 1, index: 2, spilled: true},
		{name: "past_end_data_spilled", row: 2, index: 2, spilled: true},
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
			if tc.index < 0 || tc.index >= 2 {
				// The dependency treats invalid indices as empty cells.
				// They must never panic or return data from another shared string.
				if err != nil || (tc.row == 1 && len(header) != 0) || (tc.row == 2 && len(row) != 0) {
					t.Fatalf("invalid shared string index %d: header=%v row=%v err=%v", tc.index, header, row, err)
				}
				return
			}
			if err != nil || !ok || len(header) != 1 || header[0] != "header" || len(row) != 1 || row[0] != "value" {
				t.Fatalf("valid workbook: header=%v row=%v ok=%v err=%v", header, row, ok, err)
			}
		})
	}
}

// GHSA-5h23-36rv-pm65: GetStyle must ignore negative font, fill and border IDs
// from a workbook instead of indexing their tables. Check the dependency API
// directly so this regression does not pass solely because Novera's streaming
// reader currently avoids style extraction.
func TestXLSXNegativeStyleIndicesAreValidated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		attrs string
	}{
		{name: "valid", attrs: `fontId="0" fillId="0" borderId="0"`},
		{name: "negative_font", attrs: `fontId="-1" fillId="0" borderId="0"`},
		{name: "negative_fill", attrs: `fontId="0" fillId="-1" borderId="0"`},
		{name: "negative_border", attrs: `fontId="0" fillId="0" borderId="-1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := workbookWithStyleIndices(t, tc.attrs)
			book, err := openWorkbookWithLimits(path, maxXlsxBytes, maxXlsxUncompressedBytes, maxXlsxXMLMemoryBytes)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = book.Close() })
			styleID, err := book.GetCellStyle("Sheet1", "A2")
			if err != nil || styleID != 1 {
				t.Fatalf("fixture style ID = %d, err = %v; want 1", styleID, err)
			}
			style, err := book.GetStyle(styleID)
			if err != nil || style == nil {
				t.Fatalf("style with %s: style=%v err=%v", tc.attrs, style, err)
			}
			header, iter, err := openTableIter(path, filepath.Base(path), "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(iter.close)
			row, ok, err := iter.next()
			if err != nil || !ok || len(header) != 1 || header[0] != "header" || len(row) != 1 || row[0] != "value" {
				t.Fatalf("styled workbook: header=%v row=%v ok=%v err=%v", header, row, ok, err)
			}
		})
	}
}

func workbookWithStyleIndices(t *testing.T, attrs string) string {
	t.Helper()
	book := excelize.NewFile()
	defer book.Close()
	for cell, value := range map[string]string{"A1": "header", "A2": "value"} {
		if err := book.SetCellStr("Sheet1", cell, value); err != nil {
			t.Fatal(err)
		}
	}
	styleID, err := book.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil || styleID != 1 {
		t.Fatalf("new fixture style ID = %d, err = %v; want 1", styleID, err)
	}
	if err := book.SetCellStyle("Sheet1", "A2", "A2", styleID); err != nil {
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
	patched := false
	for _, file := range zr.File {
		if file.Name != "xl/styles.xml" {
			if err := zw.Copy(file); err != nil {
				t.Fatal(err)
			}
			continue
		}
		patched = true
		w, err := zw.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		styleXML := fmt.Sprintf(`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="1"><fill><patternFill patternType="none"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" %s xfId="0" applyFont="1" applyFill="1" applyBorder="1"/></cellXfs></styleSheet>`, attrs)
		if _, err := io.WriteString(w, styleXML); err != nil {
			t.Fatal(err)
		}
	}
	if !patched {
		t.Fatal("fixture does not contain styles.xml")
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "style-indices.xlsx")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
