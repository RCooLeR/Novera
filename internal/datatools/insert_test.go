package datatools

import (
	"errors"
	"strings"
	"testing"
)

func TestAppendBoundedSQLStatementRejectsAggregateOverflow(t *testing.T) {
	var statement strings.Builder
	if err := appendBoundedSQLStatement(&statement, "123456", 8); err != nil {
		t.Fatal(err)
	}
	if err := appendBoundedSQLStatement(&statement, "789", 8); !errors.Is(err, ErrSQLStatementTooLong) {
		t.Fatalf("error = %v, want ErrSQLStatementTooLong", err)
	}
	if got := statement.String(); got != "123456" {
		t.Fatalf("statement changed on rejected append: %q", got)
	}
}

func TestDumpTableToCSV_MysqldumpInsert(t *testing.T) {
	dump := "CREATE TABLE `t` (id int, name varchar(20), email varchar(50));\n" +
		"INSERT INTO `t` VALUES (1,'Ada','a@x'),(2,'Bob',NULL),(3,'O\\'Neil','y');\n" +
		"INSERT INTO `t` VALUES (4,'tab\\there','z');\n" +
		"INSERT INTO `other` VALUES (9,'ignore','me');\n"
	var out strings.Builder
	rows, found, err := DumpTableToCSV(strings.NewReader(dump), "t", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("table t not found")
	}
	if rows != 4 {
		t.Fatalf("rows = %d, want 4", rows)
	}
	want := "col1,col2,col3\n" +
		"1,Ada,a@x\n" +
		"2,Bob,\n" + // NULL → empty
		"3,O'Neil,y\n" + // \' → '
		"4,tab\there,z\n" // \t → real tab (Go csv doesn't quote tabs)
	if out.String() != want {
		t.Fatalf("CSV mismatch:\n got %q\nwant %q", out.String(), want)
	}
}

func TestDumpTableToCSV_InsertWithColumns(t *testing.T) {
	dump := "INSERT INTO `u` (`id`, `name`) VALUES (1,'a'),(2,'b, with comma');\n"
	var out strings.Builder
	rows, found, err := DumpTableToCSV(strings.NewReader(dump), "u", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !found || rows != 2 {
		t.Fatalf("found=%v rows=%d, want true/2", found, rows)
	}
	want := "id,name\n1,a\n2,\"b, with comma\"\n" // comma inside string stays one field, csv-quoted
	if out.String() != want {
		t.Fatalf("CSV mismatch:\n got %q\nwant %q", out.String(), want)
	}
}

func TestStatementComplete(t *testing.T) {
	if statementComplete("INSERT INTO t VALUES (1, 'a;b'") {
		t.Error("a ';' inside a string must not complete the statement")
	}
	if !statementComplete("INSERT INTO t VALUES (1, 'a;b');") {
		t.Error("trailing ; should complete the statement")
	}
}
