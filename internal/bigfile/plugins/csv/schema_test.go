package csv

import (
	"strings"
	"testing"
)

func TestClassifyLeadingZeroIntIsText(t *testing.T) {
	cases := map[string]schemaKind{
		"007":   schemaText,
		"00123": schemaText,
		"-0042": schemaText,
		"0":     schemaInt,
		"-0":    schemaInt,
		"42":    schemaInt,
		"3.14":  schemaFloat,
		"true":  schemaBool,
		"hello": schemaText,
	}
	for value, want := range cases {
		if got := classifySchemaValue(value); got != want {
			t.Fatalf("classifySchemaValue(%q) = %d, want %d", value, got, want)
		}
	}
}

func TestInferSchemaKeepsLeadingZeroColumnAsText(t *testing.T) {
	report, err := InferSchema(strings.NewReader("zip\n007\n012\n10000\n"), SchemaOptions{HasHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Columns) != 1 || report.Columns[0].SQLType != SQLTypeText {
		t.Fatalf("columns = %+v, want a single TEXT column to preserve leading zeros", report.Columns)
	}
}

func TestInferSchemaDetectsHeaderAndSQLTypes(t *testing.T) {
	report, err := InferSchema(strings.NewReader("id,price,active,note\n1,12.50,true,hello\n2,14,false,NULL\n"), SchemaOptions{
		HasHeader:  true,
		NullValues: []string{"NULL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.RecordsScanned != 3 {
		t.Fatalf("records scanned = %d, want 3", report.RecordsScanned)
	}
	assertSchemaColumn(t, report.Columns[0], "id", SQLTypeBigInt, 2, 0)
	assertSchemaColumn(t, report.Columns[1], "price", SQLTypeDouble, 2, 0)
	assertSchemaColumn(t, report.Columns[2], "active", SQLTypeBoolean, 2, 0)
	assertSchemaColumn(t, report.Columns[3], "note", SQLTypeText, 1, 1)
}

func TestInferSchemaUsesDefaultColumnNamesWithoutHeader(t *testing.T) {
	report, err := InferSchema(strings.NewReader("1\tAda\n2\tGrace\n"), SchemaOptions{
		Delimiter: '\t',
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSchemaColumn(t, report.Columns[0], "column_1", SQLTypeBigInt, 2, 0)
	assertSchemaColumn(t, report.Columns[1], "column_2", SQLTypeText, 2, 0)
}

func TestInferSchemaPromotesMixedValuesToText(t *testing.T) {
	report, err := InferSchema(strings.NewReader("value\n1\nx\n"), SchemaOptions{
		HasHeader: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSchemaColumn(t, report.Columns[0], "value", SQLTypeText, 2, 0)
}

func TestInferSchemaHonorsBoundedSample(t *testing.T) {
	report, err := InferSchema(strings.NewReader("id,name\n1,Ada\n2,Grace\n"), SchemaOptions{
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

func TestInferSchemaRequiresReader(t *testing.T) {
	if _, err := InferSchema(nil, SchemaOptions{}); err == nil {
		t.Fatal("expected nil reader error")
	}
}

func TestFormatSchemaReportIncludesBoundedPreview(t *testing.T) {
	report, err := InferSchema(strings.NewReader("id,note\n1,"+strings.Repeat("x", 80)+"\n"), SchemaOptions{
		HasHeader: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	formatted := FormatSchemaReport(report)
	for _, want := range []string{
		"Columns: 2",
		"Header row: true",
		"1. id | BIGINT",
		"2. note | TEXT",
		"...",
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted report = %q, want substring %q", formatted, want)
		}
	}
	if strings.Contains(formatted, strings.Repeat("x", 80)) {
		t.Fatalf("formatted report contains unbounded sample value: %q", formatted)
	}
}

func TestFormatSchemaReportCapsDisplayedColumns(t *testing.T) {
	report := SchemaReport{
		Columns: make([]SchemaColumn, maxSchemaPreviewColumns+2),
	}
	for i := range report.Columns {
		report.Columns[i] = SchemaColumn{
			Name:    "c",
			SQLType: SQLTypeText,
		}
	}
	formatted := FormatSchemaReport(report)
	if !strings.Contains(formatted, "... 2 more columns omitted from preview") {
		t.Fatalf("formatted report = %q, want omitted column notice", formatted)
	}
}

func assertSchemaColumn(t *testing.T, got SchemaColumn, name, sqlType string, nonNull, nulls int) {
	t.Helper()
	if got.Name != name || got.SQLType != sqlType || got.NonNullCount != nonNull || got.NullCount != nulls {
		t.Fatalf("column = %+v, want name=%q type=%q nonNull=%d nulls=%d", got, name, sqlType, nonNull, nulls)
	}
}
