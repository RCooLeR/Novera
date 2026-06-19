package datatools

import (
	"strings"
	"testing"
)

func TestInferCSVSchema(t *testing.T) {
	csv := "id,price,active,name\n1,9.99,true,alice\n2,3,false,bob\n3,,true,\n"
	got, err := InferCSVSchema(strings.NewReader(csv), ',', 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Columns) != 4 {
		t.Fatalf("columns = %d, want 4", len(got.Columns))
	}
	want := []string{"BIGINT", "DOUBLE", "BOOLEAN", "TEXT"}
	for i, c := range got.Columns {
		if c.Type != want[i] {
			t.Errorf("col %s type = %s, want %s", c.Name, c.Type, want[i])
		}
	}
	// price has a mix of int + float → DOUBLE; name has one empty → 1 null.
	if got.Columns[3].Null != 1 {
		t.Errorf("name null count = %d, want 1", got.Columns[3].Null)
	}
	if got.RowsScanned != 3 {
		t.Errorf("rowsScanned = %d, want 3", got.RowsScanned)
	}
}

func TestConvertCSVToSQL(t *testing.T) {
	csvData := "id,name\n1,Ada\n2,O'Neill\n"
	opts := SQLConvertOptions{
		TableName:     "people",
		Comma:         ',',
		Columns:       []string{"id", "name"},
		ColumnTypes:   []string{"BIGINT", "TEXT"},
		IncludeCreate: true,
		BatchSize:     500,
	}
	out, err := PreviewCSVToSQL(strings.NewReader(csvData), opts, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CREATE TABLE IF NOT EXISTS `people`") {
		t.Errorf("missing CREATE TABLE:\n%s", out)
	}
	if !strings.Contains(out, "`id` BIGINT") || !strings.Contains(out, "`name` TEXT") {
		t.Errorf("missing typed columns:\n%s", out)
	}
	if !strings.Contains(out, "INSERT INTO `people` (`id`, `name`) VALUES") {
		t.Errorf("missing INSERT:\n%s", out)
	}
	// Single quotes must be doubled.
	if !strings.Contains(out, "'O''Neill'") {
		t.Errorf("quote not escaped:\n%s", out)
	}
}

func TestPlanDumpRanges(t *testing.T) {
	sum := DumpSummary{Tables: []DumpTable{
		{Name: "users", CreateOffset: 100, InsertOffset: 200},
		{Name: "orders", CreateOffset: 500, InsertOffset: 600},
	}}
	ranges := PlanDumpRanges(sum, 1000)
	if len(ranges) != 2 {
		t.Fatalf("ranges = %d, want 2", len(ranges))
	}
	if ranges[0].Name != "users" || ranges[0].Start != 100 || ranges[0].End != 500 {
		t.Errorf("users range wrong: %+v", ranges[0])
	}
	if ranges[1].Name != "orders" || ranges[1].Start != 500 || ranges[1].End != 1000 {
		t.Errorf("orders range wrong: %+v", ranges[1])
	}
}

func TestAnalyzeSQLDump(t *testing.T) {
	dump := "-- MySQL dump 10.13  mysqldump\n" +
		"/* a block\ncomment with INSERT INTO fake */\n" +
		"CREATE TABLE `users` (\n  id INT\n);\n" +
		"INSERT INTO `users` VALUES (1),(2);\n" +
		"-- INSERT INTO commented_out VALUES (9);\n" +
		"CREATE TABLE orders (id INT);\n" +
		"INSERT INTO orders VALUES (1);\n" +
		"INSERT INTO orders VALUES (2);\n"
	sum, err := AnalyzeSQLDump(strings.NewReader(dump), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Mysqldump {
		t.Error("expected mysqldump detection")
	}
	if len(sum.Tables) != 2 {
		t.Fatalf("tables = %d (%+v), want 2", len(sum.Tables), sum.Tables)
	}
	if sum.CreateTables != 2 || sum.InsertTables != 3 {
		t.Errorf("counts: create=%d insert=%d, want 2/3", sum.CreateTables, sum.InsertTables)
	}
	if sum.Tables[0].Name != "users" || sum.Tables[0].CreateOffset < 0 || sum.Tables[0].InsertOffset < 0 {
		t.Errorf("users table wrong: %+v", sum.Tables[0])
	}
	// The commented-out and in-block-comment INSERTs must NOT be counted.
}
