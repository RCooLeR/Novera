package csv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectColumnsReordersAndRemovesColumns(t *testing.T) {
	var out strings.Builder
	summary, err := ProjectColumns(context.Background(), strings.NewReader("id,name,city\n1,Ada,London\n2,Grace,NYC\n"), &out, ProjectOptions{
		Columns: []int{2, 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "city,id\nLondon,1\nNYC,2\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if summary.RecordsRead != 3 || summary.RecordsWritten != 3 {
		t.Fatalf("summary records = %d/%d, want 3/3", summary.RecordsRead, summary.RecordsWritten)
	}
	if summary.ColumnsWritten != 2 {
		t.Fatalf("columns written = %d, want 2", summary.ColumnsWritten)
	}
	if summary.Delimiter != ',' {
		t.Fatalf("delimiter = %q, want comma", summary.Delimiter)
	}
}

func TestProjectColumnsHandlesTSV(t *testing.T) {
	var out strings.Builder
	_, err := ProjectColumns(context.Background(), strings.NewReader("id\tname\tcity\n1\tAda\tLondon\n"), &out, ProjectOptions{
		Delimiter: '\t',
		Columns:   []int{1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "name\tcity\nAda\tLondon\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestProjectColumnsRejectsMissingColumnByDefault(t *testing.T) {
	var out strings.Builder
	_, err := ProjectColumns(context.Background(), strings.NewReader("id,name,city\n1,Ada\n"), &out, ProjectOptions{
		Columns: []int{0, 2},
	})
	if err == nil {
		t.Fatal("expected missing-column error")
	}
	if !strings.Contains(err.Error(), "record 2") || !strings.Contains(err.Error(), "column 2") {
		t.Fatalf("error = %q, want record and column context", err)
	}
}

func TestProjectColumnsCanFillMissingColumns(t *testing.T) {
	var out strings.Builder
	_, err := ProjectColumns(context.Background(), strings.NewReader("id,name,city\n1,Ada\n"), &out, ProjectOptions{
		Columns:           []int{0, 2},
		AllowShortRecords: true,
		MissingValue:      "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "id,city\n1,\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPreviewProjectedColumnsReordersRowsWithoutWritingOutput(t *testing.T) {
	report, err := PreviewProjectedColumns(strings.NewReader("id,name,city\n1,Ada,London\n2,Grace,NYC\n"), ProjectPreviewOptions{
		Columns: []int{2, 0},
		MaxRows: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.RecordsRead != 2 || report.ColumnsWritten != 2 {
		t.Fatalf("report = %+v, want two records and two columns", report)
	}
	if got, want := FormatProjectPreviewReport(report), "Projected rows shown: 2"; !strings.Contains(got, want) {
		t.Fatalf("formatted report = %q, want substring %q", got, want)
	}
	formatted := FormatProjectPreviewReport(report)
	if !strings.Contains(formatted, "city,id") || !strings.Contains(formatted, "London,1") {
		t.Fatalf("formatted report = %q, want projected output sample", formatted)
	}
}

func TestPreviewProjectedColumnsHonorsBoundsAndMissingPolicy(t *testing.T) {
	report, err := PreviewProjectedColumns(strings.NewReader("id\tname\tcity\n1\tAda\n2\tGrace\tNYC\n"), ProjectPreviewOptions{
		Delimiter:         '\t',
		Columns:           []int{0, 2},
		AllowShortRecords: true,
		MissingValue:      "MISSING",
		MaxBytes:          14,
		MaxRows:           10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.TruncatedSample || report.BytesScanned != 14 {
		t.Fatalf("report = %+v, want bounded truncated sample", report)
	}
	formatted := FormatProjectPreviewReport(report)
	if !strings.Contains(formatted, "id\tcity") || !strings.Contains(formatted, "sample limited to 14 bytes") {
		t.Fatalf("formatted report = %q, want TSV sample and bound warning", formatted)
	}
}

func TestPreviewProjectedColumnsOmitsTrailingPartialRecord(t *testing.T) {
	report, err := PreviewProjectedColumns(strings.NewReader("id,name\n1,\"unterminated value"), ProjectPreviewOptions{
		Columns:  []int{0, 1},
		MaxBytes: 20,
		MaxRows:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 1 || report.Rows[0][0] != "id" || report.Rows[0][1] != "name" {
		t.Fatalf("rows = %#v, want only complete header row", report.Rows)
	}
	formatted := FormatProjectPreviewReport(report)
	if !strings.Contains(formatted, "trailing partial record omitted") {
		t.Fatalf("formatted report = %q, want partial-record warning", formatted)
	}
}

func TestPreviewProjectedColumnsRejectsMissingColumnByDefault(t *testing.T) {
	_, err := PreviewProjectedColumns(strings.NewReader("id,name\n1,Ada\n"), ProjectPreviewOptions{
		Columns: []int{2},
	})
	if err == nil {
		t.Fatal("expected missing-column error")
	}
	if !strings.Contains(err.Error(), "record 1") || !strings.Contains(err.Error(), "column 2") {
		t.Fatalf("error = %q, want record and column context", err)
	}
}

func TestProjectColumnsRequiresColumns(t *testing.T) {
	var out strings.Builder
	if _, err := ProjectColumns(context.Background(), strings.NewReader("a,b\n"), &out, ProjectOptions{}); err == nil {
		t.Fatal("expected columns-required error")
	}
}

func TestProjectColumnsFileDeletesPartialOutputOnCancel(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	output := filepath.Join(dir, "output.csv")
	if err := os.WriteFile(input, []byte("id,name\n1,Ada\n2,Grace\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, err := ProjectColumnsFile(ctx, input, output, ProjectOptions{
		Columns: []int{0},
		Progress: func(ProjectProgress) {
			cancel()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial output still exists: %v", statErr)
	}
}

func TestProjectColumnsFileRejectsSamePath(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	if err := os.WriteFile(input, []byte("id,name\n1,Ada\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectColumnsFile(context.Background(), input, input, ProjectOptions{Columns: []int{0}}); err == nil {
		t.Fatal("expected same-path error")
	}
}

func TestProjectColumnsFileRejectsExistingOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	output := filepath.Join(dir, "output.csv")
	if err := os.WriteFile(input, []byte("id,name\n1,Ada\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("preexisting"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectColumnsFile(context.Background(), input, output, ProjectOptions{Columns: []int{0}}); err == nil {
		t.Fatal("expected existing-output error")
	}
	if data, err := os.ReadFile(output); err != nil || string(data) != "preexisting" {
		t.Fatalf("preexisting output changed, data=%q err=%v", data, err)
	}
}
