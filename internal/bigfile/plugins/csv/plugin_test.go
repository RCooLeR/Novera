package csv

import (
	"context"
	"strings"
	"testing"

	"novera/internal/bigfile/plugins"
)

func TestCSVDescriptorUsesTruthfulPerOperationCapabilities(t *testing.T) {
	descriptor := Plugin()
	if err := descriptor.Validate(); err != nil {
		t.Fatal(err)
	}
	if descriptor.HugeFileSafe {
		t.Fatal("CSV descriptor must not make a plugin-wide huge-file-safety claim")
	}
	operations := make(map[string]plugins.OperationCapability, len(descriptor.Operations))
	for _, operation := range descriptor.Operations {
		operations[operation.ID] = operation
	}
	if got := operations["inspect-sample"]; got.Processing != plugins.ProcessingConfigurableSample || got.MaxInputBytes != 0 || got.MaxUnitBytes != MaxSampleBytes {
		t.Fatalf("inspect metadata = %+v", got)
	}
	if got := operations["project-columns-file"]; got.Processing != plugins.ProcessingStreaming || got.Memory != plugins.MemoryRecordProportional || !got.AtomicOutput {
		t.Fatalf("projection metadata = %+v", got)
	}
	if got := operations["add-column-file"]; got.Processing != plugins.ProcessingStreaming || got.Memory != plugins.MemoryRecordProportional || !got.AtomicOutput {
		t.Fatalf("add-column metadata = %+v", got)
	}
	if got := operations["deduplicate-rows"]; got.Processing != plugins.ProcessingStreaming || got.Memory != plugins.MemoryBounded || !got.AtomicOutput || !strings.Contains(strings.Join(got.Notes, " "), "refuses before") {
		t.Fatalf("dedupe metadata = %+v", got)
	}
	if got := operations["convert-to-sql-file"]; got.Memory != plugins.MemoryBatchProportional || got.MaxUnitBytes != MaxSQLInsertBatchBytes || !got.AtomicOutput {
		t.Fatalf("SQL conversion metadata = %+v", got)
	}

	descriptor.FilePatterns[0] = "*.changed"
	descriptor.Operations[0].Notes[0] = "changed"
	fresh := Plugin()
	if got := fresh.FilePatterns[0]; got != "*.csv" {
		t.Fatalf("mutating returned descriptor changed CSV patterns: %q", got)
	}
	if fresh.Operations[0].Notes[0] == "changed" {
		t.Fatal("mutating returned operation notes changed CSV descriptor metadata")
	}
}

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

	var added strings.Builder
	addSummary, err := runtime.AddColumn(context.Background(), strings.NewReader(input), &added, AddColumnOptions{
		Delimiter: ',', Position: 1, Value: "constant",
	})
	if err != nil {
		t.Fatal(err)
	}
	if addSummary.RecordsWritten != 2 || added.String() != "id,constant,name\n1,constant,Ada\n" {
		t.Fatalf("add-column summary/output = %#v/%q", addSummary, added.String())
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
