package extract

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/plugins/sql/analyze"
)

func interleavedRegionFixture() (string, analyze.Summary) {
	const source = "A-CREATE\nB-CREATE\nA-INSERT\nB-INSERT\n"
	return source, analyze.Summary{Tables: []analyze.Table{
		{
			Name: "`db1`.`records`", Database: "db1", TableName: "records", CreateOffset: 0, InsertOffset: 18,
			Regions: []analyze.Region{
				{Kind: analyze.RegionCreate, StartOffset: 0, EndOffset: 9},
				{Kind: analyze.RegionInsert, StartOffset: 18, EndOffset: 27},
			},
		},
		{
			Name: "`db2`.`records`", Database: "db2", TableName: "records", CreateOffset: 9, InsertOffset: 27,
			Regions: []analyze.Region{
				{Kind: analyze.RegionCreate, StartOffset: 9, EndOffset: 18},
				{Kind: analyze.RegionInsert, StartOffset: 27, EndOffset: int64(len(source))},
			},
		},
	}}
}

func TestPlanTableRangesRetainsQualifiedNoncontiguousRegions(t *testing.T) {
	source, summary := interleavedRegionFixture()
	ranges, err := PlanTableRanges(summary, int64(len(source)), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 {
		t.Fatalf("ranges = %+v", ranges)
	}
	if ranges[0].Name != "`db1`.`records`" || ranges[0].Bytes != 18 || len(ranges[0].Regions) != 2 {
		t.Fatalf("first range = %+v", ranges[0])
	}
	want := [][2]int64{{0, 9}, {18, 27}}
	got := ByteRanges(ranges[0])
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("byte ranges = %v, want %v", got, want)
	}
}

func TestExtractTableConcatenatesOnlyOwnedInterleavedRegions(t *testing.T) {
	sourceText, analysis := interleavedRegionFixture()
	dir := t.TempDir()
	source := filepath.Join(dir, "dump.sql")
	outputDir := filepath.Join(dir, "out")
	if err := os.WriteFile(source, []byte(sourceText), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	summary, err := ExtractTable(context.Background(), doc, source, analysis, "`db1`.`records`", WriteOptions{
		PlanOptions: PlanOptions{OutputDir: outputDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Outputs) != 1 {
		t.Fatalf("outputs = %+v", summary.Outputs)
	}
	got, err := os.ReadFile(summary.Outputs[0].OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "A-CREATE\nA-INSERT\n" {
		t.Fatalf("qualified extraction included another table: %q", got)
	}
}

func TestAnalyzedExtractionCopiesOnlyExactOwnedStatements(t *testing.T) {
	sourceText := "SET @header = 1;\n" +
		"CREATE TABLE target (id int, note varchar(32));\n" +
		"UPDATE unrelated SET note = 'between create and insert';\n" +
		"INSERT INTO target VALUES (1, 'owned;literal');\n" +
		"DELETE FROM unrelated WHERE id = 1;\n" +
		"INSERT INTO other_table VALUES (9);\n" +
		"SET @footer = 'must not be copied';\n"
	analysis, err := analyze.Analyze(context.Background(), bytes.NewReader([]byte(sourceText)), analyze.Options{ChunkSize: 11})
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Tables) != 2 {
		t.Fatalf("analysis tables = %#v", analysis.Tables)
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "dump.sql")
	outputDir := filepath.Join(dir, "out")
	if err := os.WriteFile(source, []byte(sourceText), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	summary, err := ExtractTable(context.Background(), doc, source, analysis, "target", WriteOptions{
		PlanOptions: PlanOptions{OutputDir: outputDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Outputs) != 1 {
		t.Fatalf("outputs = %#v", summary.Outputs)
	}
	got, err := os.ReadFile(summary.Outputs[0].OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE TABLE target (id int, note varchar(32));" +
		"INSERT INTO target VALUES (1, 'owned;literal');"
	if string(got) != want {
		t.Fatalf("exact extraction = %q, want %q", got, want)
	}
	for _, forbidden := range []string{"UPDATE unrelated", "DELETE FROM unrelated", "other_table", "@header", "@footer"} {
		if bytes.Contains(got, []byte(forbidden)) {
			t.Fatalf("exact extraction retained unrelated SQL %q: %q", forbidden, got)
		}
	}
}

func TestAnalyzedExtractionPreservesWordPressSerializedPayloadBytes(t *testing.T) {
	create := "CREATE TABLE `wp_options` (`option_id` bigint, `option_value` longtext);"
	insert := `INSERT INTO ` + "`wp_options`" + ` VALUES (1,'a:3:{s:4:"semi";s:4:"a;b;";s:5:"shape";s:3:"),(";s:5:"slash";s:3:"a\\b";}');`
	sourceText := "SET NAMES utf8mb4;\n" + create + "\n" + insert + "\nSET @footer = 1;\n"
	analysis, err := analyze.Analyze(context.Background(), bytes.NewReader([]byte(sourceText)), analyze.Options{ChunkSize: 1})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "wordpress.sql")
	outputDir := filepath.Join(dir, "out")
	if err := os.WriteFile(source, []byte(sourceText), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	summary, err := ExtractTable(context.Background(), doc, source, analysis, "wp_options", WriteOptions{
		PlanOptions: PlanOptions{OutputDir: outputDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Outputs) != 1 {
		t.Fatalf("outputs = %#v", summary.Outputs)
	}
	got, err := os.ReadFile(summary.Outputs[0].OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte(create + insert); !bytes.Equal(got, want) {
		t.Fatalf("serialized extraction bytes changed\n got: %q\nwant: %q", got, want)
	}
	if sourceAfter, readErr := os.ReadFile(source); readErr != nil || !bytes.Equal(sourceAfter, []byte(sourceText)) {
		t.Fatalf("extraction changed source: got %q, err %v", sourceAfter, readErr)
	}
}
