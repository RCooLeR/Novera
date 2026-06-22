package csv

import (
	"context"
	"strings"
	"testing"
)

func TestCSVRuntimePluginRoutesInspectionSchemaPreviewAndGuide(t *testing.T) {
	var runtime Runtime = RuntimePlugin()
	if runtime.Descriptor().ID != "csv" {
		t.Fatalf("descriptor id = %q, want csv", runtime.Descriptor().ID)
	}

	input := "id,name\n1,Ada\n2,Bob\n"
	delimiters, err := runtime.InspectReader(strings.NewReader(input), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if delimiters.Delimiter != ',' || delimiters.Columns != 2 {
		t.Fatalf("delimiter report = %#v", delimiters)
	}

	schema, err := runtime.InferSchema(strings.NewReader(input), SchemaOptions{HasHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Columns) != 2 || schema.Columns[0].Name != "id" {
		t.Fatalf("schema = %#v", schema)
	}

	preview, err := runtime.PreviewRows(strings.NewReader(input), PreviewOptions{HasHeader: true, MaxRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rows) != 1 || preview.Rows[0][1] != "Ada" {
		t.Fatalf("preview = %#v", preview)
	}

	guide, err := runtime.BuildColumnGuide(strings.NewReader(input), ColumnGuideOptions{HasHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	if guide.ProjectionText != "1,2" {
		t.Fatalf("guide projection = %q, want 1,2", guide.ProjectionText)
	}

	projectPreview, err := runtime.PreviewProjectedColumns(strings.NewReader(input), ProjectPreviewOptions{
		Columns: []int{1, 0},
		MaxRows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(projectPreview.Rows) != 1 || projectPreview.Rows[0][0] != "name" || projectPreview.Rows[0][1] != "id" {
		t.Fatalf("project preview = %#v", projectPreview)
	}
}

func TestCSVRuntimePluginRoutesProjectionAndSQLConversion(t *testing.T) {
	var runtime Runtime = RuntimePlugin()
	input := "id,name\n1,Ada\n"

	var projected strings.Builder
	summary, err := runtime.ProjectColumns(context.Background(), strings.NewReader(input), &projected, ProjectOptions{
		Columns: []int{1, 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.RecordsWritten != 2 || !strings.Contains(projected.String(), "name,id") {
		t.Fatalf("project summary/output = %#v/%q", summary, projected.String())
	}

	var sql strings.Builder
	convertSummary, err := runtime.ConvertToSQL(context.Background(), strings.NewReader(input), &sql, SQLConvertOptions{
		HasHeader:       true,
		TableName:       "users",
		InsertBatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if convertSummary.RowsWritten != 1 || !strings.Contains(sql.String(), "INSERT INTO `users`") {
		t.Fatalf("sql summary/output = %#v/%q", convertSummary, sql.String())
	}
}
