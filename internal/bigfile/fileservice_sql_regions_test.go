package bigfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlanalyze "novera/internal/bigfile/plugins/sql/analyze"
	sqlextract "novera/internal/bigfile/plugins/sql/extract"
)

func TestSQLExtractionRegionsRejectMissingStatementKind(t *testing.T) {
	const sourceSize int64 = 128
	createOnly := sqlanalyze.Summary{Tables: []sqlanalyze.Table{{
		Name: "created", CreateOffset: 0, InsertOffset: -1,
	}}}
	insertOnly := sqlanalyze.Summary{Tables: []sqlanalyze.Table{{
		Name: "inserted", CreateOffset: -1, InsertOffset: 0,
	}}}

	if _, err := planDataExtraction(createOnly, sourceSize, "created"); err == nil || !strings.Contains(err.Error(), "no INSERT or REPLACE data") {
		t.Fatalf("create-only data extraction error = %v, want no INSERT or REPLACE data", err)
	}
	regions, name, err := planSchemaExtraction(createOnly, sourceSize, "created")
	if err != nil {
		t.Fatal(err)
	}
	if name != "created.schema.sql" || len(regions) != 1 || regions[0] != [2]int64{0, sourceSize} {
		t.Fatalf("create-only schema plan = %v, %q", regions, name)
	}

	if _, _, err := planSchemaExtraction(insertOnly, sourceSize, "inserted"); err == nil || !strings.Contains(err.Error(), "no CREATE TABLE schema") {
		t.Fatalf("insert-only schema extraction error = %v, want no CREATE TABLE schema", err)
	}
	if _, _, err := planSchemaExtraction(insertOnly, sourceSize, ""); err == nil || !strings.Contains(err.Error(), "no CREATE TABLE schema") {
		t.Fatalf("insert-only whole-schema error = %v, want no CREATE TABLE schema", err)
	}
	regions, err = planDataExtraction(insertOnly, sourceSize, "inserted")
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 1 || regions[0] != [2]int64{0, sourceSize} {
		t.Fatalf("insert-only data plan = %v", regions)
	}
}

func TestSQLServiceSplitUsesExactQualifiedRegions(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "qualified-split.sql")
	sourceText := "CREATE TABLE `db1`.`records` (`id` int);\n" +
		"CREATE TABLE `db2`.`records` (`id` int);\n" +
		"INSERT INTO `db1`.`records` VALUES (1);\n" +
		"UPDATE `db1`.`records` SET `id` = 9;\n" +
		"INSERT INTO `db2`.`records` VALUES (2);\n" +
		"INSERT INTO `db1`.`records` VALUES (3);\n"
	if err := os.WriteFile(source, []byte(sourceText), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	if _, err := service.SqlAnalyze(meta.FileID); err != nil {
		t.Fatal(err)
	}

	outputDir := filepath.Join(dir, "split")
	previousDialog := sqlDirDialog
	sqlDirDialog = func(string) (string, error) { return outputDir, nil }
	t.Cleanup(func() { sqlDirDialog = previousDialog })
	if _, err := service.SqlSplitByTableViaDialog(meta.FileID); err != nil {
		t.Fatal(err)
	}

	checks := map[string]string{
		"`db1`.`records`": "CREATE TABLE `db1`.`records` (`id` int);" +
			"INSERT INTO `db1`.`records` VALUES (1);" +
			"INSERT INTO `db1`.`records` VALUES (3);",
		"`db2`.`records`": "CREATE TABLE `db2`.`records` (`id` int);" +
			"INSERT INTO `db2`.`records` VALUES (2);",
	}
	for identity, want := range checks {
		path := filepath.Join(outputDir, sqlextract.SafeFilenameComponent(identity)+".sql")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read split output for %s: %v", identity, err)
		}
		if string(got) != want {
			t.Fatalf("split output for %s = %q, want %q", identity, got, want)
		}
		if strings.Contains(string(got), "UPDATE") {
			t.Fatalf("split output for %s retained unrelated UPDATE", identity)
		}
	}
}

func TestSQLServiceExportsOnlyQualifiedInterleavedTableRegions(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "qualified.sql")
	sourceText := "CREATE TABLE `db1`.`records` (`id` int);\n" +
		"CREATE TABLE `db2`.`records` (`id` int);\n" +
		"INSERT INTO `db1`.`records` VALUES (1);\n" +
		"INSERT INTO `db2`.`records` VALUES (2);\n" +
		"INSERT INTO `db1`.`records` VALUES (3);\n"
	if err := os.WriteFile(source, []byte(sourceText), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewFileService()
	meta, err := svc.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.CloseFile(meta.FileID) })
	if _, err := svc.SqlAnalyze(meta.FileID); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(dir, "table.sql")
	previousDialog := sqlSaveDialog
	sqlSaveDialog = func(string, string) (string, error) { return output, nil }
	t.Cleanup(func() { sqlSaveDialog = previousDialog })
	qualified := "`db1`.`records`"
	if _, err := svc.SqlExtractTableViaDialog(meta.FileID, qualified); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE TABLE `db1`.`records` (`id` int);INSERT INTO `db1`.`records` VALUES (1);INSERT INTO `db1`.`records` VALUES (3);"
	if string(got) != want {
		t.Fatalf("qualified table output = %q, want %q", got, want)
	}

	output = filepath.Join(dir, "data.sql")
	if _, err := svc.SqlExtractDataViaDialog(meta.FileID, qualified); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want = "INSERT INTO `db1`.`records` VALUES (1);INSERT INTO `db1`.`records` VALUES (3);"
	if string(got) != want {
		t.Fatalf("qualified data output = %q, want %q", got, want)
	}

	output = filepath.Join(dir, "schema.sql")
	if _, err := svc.SqlExtractSchemaViaDialog(meta.FileID, qualified); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want = "CREATE TABLE `db1`.`records` (`id` int);"
	if string(got) != want {
		t.Fatalf("qualified schema output = %q, want %q", got, want)
	}
	if sourceAfter, err := os.ReadFile(source); err != nil || string(sourceAfter) != sourceText {
		t.Fatalf("source changed: %q, %v", sourceAfter, err)
	}
}
