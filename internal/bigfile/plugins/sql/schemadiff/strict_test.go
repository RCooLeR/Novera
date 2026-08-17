package schemadiff

import (
	"strings"
	"testing"
)

func TestParseColumnsStrictHandlesCommentsEscapedIdentifiersAndTrailingRange(t *testing.T) {
	ddl := []byte("\xef\xbb\xbfCREATE TABLE `app`.`items` (\r\n" +
		"  `a``b` VARCHAR ( 20 ) /* ordinary comment */ NOT NULL,\r\n" +
		"  -- column comment\r\n" +
		"  \"Case\" decimal(10, 2) DEFAULT 'A  B',\r\n" +
		"  CONSTRAINT `fk` FOREIGN KEY (`a``b`) REFERENCES `other` (`id`)\r\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\r\n" +
		"LOCK TABLES `items` WRITE;\r\n")
	columns, err := ParseColumnsStrict(ddl)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 {
		t.Fatalf("columns = %+v, want 2", columns)
	}
	if columns[0].Name != "a`b" || columns[0].Definition != "VARCHAR(20) NOT NULL" {
		t.Fatalf("first column = %+v", columns[0])
	}
	if columns[1].Name != "Case" || columns[1].Definition != "decimal(10,2) DEFAULT 'A  B'" {
		t.Fatalf("second column = %+v", columns[1])
	}
}

func TestStrictDiffIgnoresSyntaxWhitespaceButPreservesLiteralBytes(t *testing.T) {
	oldDDL := []byte("CREATE TABLE t (id INT NOT NULL, note VARCHAR(20) DEFAULT 'A  B');")
	oldColumns, err := ParseColumnsStrict(oldDDL)
	if err != nil {
		t.Fatal(err)
	}
	cosmeticDDL := []byte("create table t(/*x*/ id int not null,note varchar ( 20 ) default 'A  B');")
	cosmeticColumns, err := ParseColumnsStrict(cosmeticDDL)
	if err != nil {
		t.Fatal(err)
	}
	if result := Diff(
		[]Table{{Name: "t", Columns: oldColumns, Definition: ParseTable(oldDDL)}},
		[]Table{{Name: "t", Columns: cosmeticColumns, Definition: ParseTable(cosmeticDDL)}},
	); result.UnchangedCount != 1 || len(result.ChangedTables) != 0 {
		t.Fatalf("cosmetic formatting changed schema: %+v", result)
	}

	changedDDL := []byte("CREATE TABLE t (id INT NOT NULL, note VARCHAR(20) DEFAULT 'a b');")
	changedColumns, err := ParseColumnsStrict(changedDDL)
	if err != nil {
		t.Fatal(err)
	}
	result := Diff(
		[]Table{{Name: "t", Columns: oldColumns, Definition: ParseTable(oldDDL)}},
		[]Table{{Name: "t", Columns: changedColumns, Definition: ParseTable(changedDDL)}},
	)
	if len(result.ChangedTables) != 1 || len(result.ChangedTables[0].ChangedColumns) != 1 {
		t.Fatalf("literal byte change was hidden: %+v", result)
	}
}

func TestParseColumnsStrictRejectsIncompleteAmbiguousAndDuplicateDDL(t *testing.T) {
	tests := []string{
		"CREATE TABLE t (id int)",
		"CREATE TABLE t (id int,);",
		"CREATE TABLE t (id int, id bigint);",
		"CREATE TABLE t (id varchar(20) DEFAULT 'unterminated);",
		"CREATE TABLE t (id varchar(20) DEFAULT 'a\\\\b');",
		"CREATE TABLE t ([id] int);",
		"CREATE TABLE t (id int /*!40101 NOT NULL */);",
		"CREATE TABLE t (LIKE other);",
		"CREATE TABLE t (id decimal((10,2));",
	}
	for _, ddl := range tests {
		t.Run(ddl, func(t *testing.T) {
			if columns, err := ParseColumnsStrict([]byte(ddl)); err == nil {
				t.Fatalf("unsafe DDL parsed as %+v", columns)
			}
			if columns := ParseColumns([]byte(ddl)); columns != nil {
				t.Fatalf("compatibility parser returned partial columns: %+v", columns)
			}
		})
	}
}

func TestParseColumnsStrictEnforcesBudgets(t *testing.T) {
	tooLongIdentifier := strings.Repeat("x", MaxIdentifierBytes+1)
	if _, err := ParseColumnsStrict([]byte("CREATE TABLE t (" + tooLongIdentifier + " int);")); err == nil {
		t.Fatal("oversized identifier was accepted")
	}
	if _, err := ParseColumnsStrict([]byte("CREATE TABLE t (" + strings.Repeat(" ", MaxStatementBytes) + "id int);")); err == nil {
		t.Fatal("oversized statement was accepted")
	}
	if _, err := ParseColumnsStrict([]byte("CREATE TABLE t (\xff int);")); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
}

func TestStrictDiffDoesNotFoldColumnIdentityCase(t *testing.T) {
	oldDDL := []byte("CREATE TABLE t (Value int);")
	oldColumns, err := ParseColumnsStrict(oldDDL)
	if err != nil {
		t.Fatal(err)
	}
	newDDL := []byte("CREATE TABLE t (value int);")
	newColumns, err := ParseColumnsStrict(newDDL)
	if err != nil {
		t.Fatal(err)
	}
	result := Diff(
		[]Table{{Name: "t", Columns: oldColumns, Definition: ParseTable(oldDDL)}},
		[]Table{{Name: "t", Columns: newColumns, Definition: ParseTable(newDDL)}},
	)
	if len(result.ChangedTables) != 1 ||
		len(result.ChangedTables[0].AddedColumns) != 1 ||
		len(result.ChangedTables[0].RemovedColumns) != 1 {
		t.Fatalf("case-distinct columns were folded together: %+v", result)
	}
}
