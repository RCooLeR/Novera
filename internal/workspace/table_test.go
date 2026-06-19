package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeCSV builds a CSV with an id,name header and n data rows (id = 0..n-1).
func writeCSV(t *testing.T, root string, n int) {
	t.Helper()
	var b []byte
	b = append(b, "id,name\n"...)
	for i := 0; i < n; i++ {
		b = append(b, fmt.Sprintf("%d,row-%d\n", i, i)...)
	}
	if err := os.WriteFile(filepath.Join(root, "data.csv"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSvc(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	writeCSV(t, root, 5000)
	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close) // release the browse cursor's file handle before TempDir cleanup
	return s
}

func TestQueryTableBrowsePaging(t *testing.T) {
	s := newSvc(t)

	p, err := s.QueryTable("data.csv", TableQuery{Offset: 0, Limit: 100, SortCol: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Columns) != 2 || p.Columns[0] != "id" {
		t.Fatalf("header wrong: %v", p.Columns)
	}
	if len(p.Rows) != 100 || p.Rows[0][0] != "0" || p.Rows[99][0] != "99" {
		t.Fatalf("first page wrong: got %d rows, first=%v", len(p.Rows), p.Rows[0])
	}
	if !p.HasMore {
		t.Error("expected HasMore on first page of 5000-row file")
	}
	if p.TotalRows != -1 {
		t.Errorf("browse mode should not know total; got %d", p.TotalRows)
	}

	// A deep window returns the right rows.
	p2, err := s.QueryTable("data.csv", TableQuery{Offset: 4990, Limit: 100, SortCol: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Rows) != 10 || p2.Rows[0][0] != "4990" || p2.HasMore {
		t.Fatalf("tail window wrong: %d rows, first=%v hasMore=%v", len(p2.Rows), p2.Rows[0], p2.HasMore)
	}
}

func TestQueryTableBrowseResume(t *testing.T) {
	s := newSvc(t) // 5000 rows
	// Page sequentially; the resume cursor must keep rows contiguous and correct.
	off, seen := 0, 0
	for {
		p, err := s.QueryTable("data.csv", TableQuery{Offset: off, Limit: 500, SortCol: -1})
		if err != nil {
			t.Fatal(err)
		}
		for i, row := range p.Rows {
			if got, _ := strconv.Atoi(row[0]); got != off+i {
				t.Fatalf("row mismatch at %d: got %s", off+i, row[0])
			}
		}
		seen += len(p.Rows)
		off += len(p.Rows)
		if len(p.Rows) == 0 || !p.HasMore {
			break
		}
	}
	if seen != 5000 {
		t.Fatalf("sequential paging read %d rows, want 5000", seen)
	}
}

func TestQueryTableFilterWholeFile(t *testing.T) {
	s := newSvc(t)
	// "row-42" matches exactly one row anywhere in the file.
	p, err := s.QueryTable("data.csv", TableQuery{Offset: 0, Limit: 1000, Filter: "row-42\n", SortCol: -1})
	if err != nil {
		t.Fatal(err)
	}
	_ = p
	// A filter that matches a single deep row proves the whole file was scanned.
	p, err = s.QueryTable("data.csv", TableQuery{Offset: 0, Limit: 1000, Filter: "row-4242", SortCol: -1})
	if err != nil {
		t.Fatal(err)
	}
	if p.TotalRows != 1 || len(p.Rows) != 1 || p.Rows[0][0] != "4242" {
		t.Fatalf("whole-file filter wrong: total=%d rows=%d", p.TotalRows, len(p.Rows))
	}
}

func TestTableInfoAndIndexedSeek(t *testing.T) {
	s := newSvc(t) // 5000 rows, id,name

	info, err := s.TableInfo("data.csv", "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Rows != 5000 {
		t.Fatalf("TableInfo row count = %d, want 5000", info.Rows)
	}
	if !info.Indexed {
		t.Fatal("expected a sparse index for a CSV file")
	}

	// A deep random window (not at a cursor position) must seek via the index and
	// still return the exact rows.
	p, err := s.QueryTable("data.csv", TableQuery{Offset: 3777, Limit: 5, SortCol: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 5 || p.Rows[0][0] != "3777" || p.Rows[4][0] != "3781" {
		t.Fatalf("indexed seek window wrong: %v", p.Rows)
	}
}

func TestScanCSVRowsQuotedNewlines(t *testing.T) {
	root := t.TempDir()
	// 3 data rows; row 2 has a quoted field containing a newline + a comma + an
	// escaped quote — none of which must be treated as a record boundary.
	body := "id,note\n" +
		"1,plain\n" +
		"2,\"line one\nline two, still row 2 \"\"q\"\"\"\n" +
		"3,last\n"
	if err := os.WriteFile(filepath.Join(root, "q.csv"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	info, err := s.TableInfo("q.csv", "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Rows != 3 {
		t.Fatalf("quoted-newline row count = %d, want 3", info.Rows)
	}
	p, err := s.QueryTable("q.csv", TableQuery{Offset: 0, Limit: 10, SortCol: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 3 || !strings.Contains(p.Rows[1][1], "line two") {
		t.Fatalf("quoted-newline rows parsed wrong: %v", p.Rows)
	}
}

func TestQueryTableDelimiterDetectAndOverride(t *testing.T) {
	root := t.TempDir()
	// A semicolon-separated file with a .csv extension (common in EU locales).
	body := "id;name;city\n1;alice;NYC\n2;bob;LA\n"
	if err := os.WriteFile(filepath.Join(root, "semi.csv"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	// Auto-detect: should pick semicolon → 3 columns.
	p, err := s.QueryTable("semi.csv", TableQuery{Limit: 10, SortCol: -1})
	if err != nil {
		t.Fatal(err)
	}
	if p.Delimiter != "semicolon" || len(p.Columns) != 3 || p.Columns[1] != "name" {
		t.Fatalf("auto-detect failed: delim=%q cols=%v", p.Delimiter, p.Columns)
	}
	if len(p.Rows) != 2 || p.Rows[0][1] != "alice" {
		t.Fatalf("rows parsed wrong with detected delimiter: %v", p.Rows)
	}

	// Override to comma: no commas present, so the whole line is one column.
	p2, err := s.QueryTable("semi.csv", TableQuery{Limit: 10, SortCol: -1, Delimiter: "comma"})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Delimiter != "comma" || len(p2.Columns) != 1 {
		t.Fatalf("override to comma failed: delim=%q cols=%v", p2.Delimiter, p2.Columns)
	}
}

func TestQueryTableSortWholeFile(t *testing.T) {
	s := newSvc(t)
	// Descending numeric sort on id: the global max (4999) must be first, proving
	// the sort spans the whole file, not just a loaded window.
	p, err := s.QueryTable("data.csv", TableQuery{Offset: 0, Limit: 10, SortCol: 0, SortDir: -1})
	if err != nil {
		t.Fatal(err)
	}
	if p.TotalRows != 5000 {
		t.Fatalf("sorted total should be 5000, got %d", p.TotalRows)
	}
	if p.Rows[0][0] != "4999" || p.Rows[1][0] != "4998" {
		t.Fatalf("desc numeric sort wrong: %v, %v", p.Rows[0], p.Rows[1])
	}
	// Numeric-aware: "10" must sort after "9", not before (lexical would invert).
	asc, err := s.QueryTable("data.csv", TableQuery{Offset: 0, Limit: 12, SortCol: 0, SortDir: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if got, _ := strconv.Atoi(asc.Rows[i][0]); got != i {
			t.Fatalf("asc numeric sort wrong at %d: got %s", i, asc.Rows[i][0])
		}
	}
}
