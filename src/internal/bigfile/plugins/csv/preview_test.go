package csv

import (
	"strings"
	"testing"
)

func TestPreviewRowsUsesHeaderAndBoundedRows(t *testing.T) {
	report, err := PreviewRows(strings.NewReader("id,name\n1,Ada\n2,Grace\n3,Linus\n"), PreviewOptions{
		HasHeader: true,
		MaxRows:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(report.Header, ","), "id,name"; got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if len(report.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(report.Rows))
	}
	if report.RecordsScanned != 3 {
		t.Fatalf("records scanned = %d, want header + 2 rows", report.RecordsScanned)
	}
}

func TestPreviewRowsHandlesTSV(t *testing.T) {
	report, err := PreviewRows(strings.NewReader("id\tname\n1\tAda\n"), PreviewOptions{
		Delimiter: '\t',
		HasHeader: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Rows[0][1]; got != "Ada" {
		t.Fatalf("row field = %q, want Ada", got)
	}
}

func TestPreviewRowsHonorsBoundedSample(t *testing.T) {
	report, err := PreviewRows(strings.NewReader("id,name\n1,Ada\n2,Grace\n"), PreviewOptions{
		HasHeader: true,
		MaxBytes:  10,
		MaxRows:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.TruncatedSample {
		t.Fatal("expected truncated sample")
	}
	if report.BytesScanned != 10 {
		t.Fatalf("bytes scanned = %d, want 10", report.BytesScanned)
	}
}

func TestFormatPreviewReportBoundsCellsAndColumns(t *testing.T) {
	row := make([]string, maxPreviewColumns+2)
	for i := range row {
		row[i] = strings.Repeat("x", maxPreviewCellRunes+20)
	}
	report := PreviewReport{
		Rows: [][]string{row},
	}
	formatted := FormatPreviewReport(report)
	if !strings.Contains(formatted, "... 2 more columns") {
		t.Fatalf("formatted report = %q, want omitted column notice", formatted)
	}
	if strings.Contains(formatted, strings.Repeat("x", maxPreviewCellRunes+20)) {
		t.Fatalf("formatted report contains unbounded cell value: %q", formatted)
	}
}

func TestPreviewRowsRequiresReader(t *testing.T) {
	if _, err := PreviewRows(nil, PreviewOptions{}); err == nil {
		t.Fatal("expected nil reader error")
	}
}
