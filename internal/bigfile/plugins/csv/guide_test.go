package csv

import (
	"strings"
	"testing"
)

func TestBuildColumnGuideFillsProjectionAndSQLColumns(t *testing.T) {
	report, err := BuildColumnGuide(strings.NewReader("id,name,active\n1,Ada,true\n2,Grace,false\n"), ColumnGuideOptions{
		HasHeader: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectionText != "1,2,3" {
		t.Fatalf("projection text = %q, want 1,2,3", report.ProjectionText)
	}
	if report.SQLColumnsText != "id,name,active" {
		t.Fatalf("SQL columns text = %q, want id,name,active", report.SQLColumnsText)
	}
	if report.ColumnsIncluded != 3 {
		t.Fatalf("columns included = %d, want 3", report.ColumnsIncluded)
	}
}

func TestBuildColumnGuideQuotesSQLColumnList(t *testing.T) {
	report, err := BuildColumnGuide(strings.NewReader("\"last,name\",note\nAda,hello\n"), ColumnGuideOptions{
		HasHeader: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.SQLColumnsText != "\"last,name\",note" {
		t.Fatalf("SQL columns text = %q, want quoted comma field", report.SQLColumnsText)
	}
}

func TestBuildColumnGuideCapsColumns(t *testing.T) {
	report, err := BuildColumnGuide(strings.NewReader("a,b,c\n1,2,3\n"), ColumnGuideOptions{
		HasHeader:  true,
		MaxColumns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.TruncatedColumns {
		t.Fatal("expected truncated columns")
	}
	if report.ProjectionText != "1,2" {
		t.Fatalf("projection text = %q, want capped projection", report.ProjectionText)
	}
	if !strings.Contains(FormatColumnGuide(report), "column guide limited to first 2 columns") {
		t.Fatalf("formatted guide = %q, want column limit warning", FormatColumnGuide(report))
	}
}

func TestFormatColumnGuideIncludesBoundedMap(t *testing.T) {
	report, err := BuildColumnGuide(strings.NewReader("id,note\n1,"+strings.Repeat("x", 80)+"\n"), ColumnGuideOptions{
		HasHeader: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	formatted := FormatColumnGuide(report)
	for _, want := range []string{
		"Columns discovered: 2",
		"Project columns value:",
		"SQL explicit columns value:",
		"1. id | BIGINT",
		"2. note | TEXT",
		"...",
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted guide = %q, want substring %q", formatted, want)
		}
	}
	if strings.Contains(formatted, strings.Repeat("x", 80)) {
		t.Fatalf("formatted guide contains unbounded sample: %q", formatted)
	}
}
