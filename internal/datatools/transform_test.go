package datatools

import (
	"strings"
	"testing"
)

func TestTransformDump(t *testing.T) {
	dump := "CREATE DEFINER=`root`@`localhost` PROCEDURE p() BEGIN END;\n" +
		"CREATE TABLE `t` (id INT) ENGINE=MyISAM AUTO_INCREMENT=42 DEFAULT CHARSET=latin1;\n" +
		"USE `olddb`;\n" +
		"INSERT INTO `olddb`.`t` VALUES (1);\n"
	var out strings.Builder
	sum, err := TransformDump(strings.NewReader(dump), &out, DumpTransform{
		RemoveDefiner:     true,
		DropAutoIncrement: true,
		Engine:            "InnoDB",
		Charset:           "utf8mb4",
		FromDatabase:      "olddb",
		ToDatabase:        "newdb",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	checks := map[string]bool{
		"DEFINER":                 false, // removed
		"MyISAM":                  false, // replaced
		"AUTO_INCREMENT":          false, // removed
		"latin1":                  false, // replaced
		"ENGINE=InnoDB":           true,
		"DEFAULT CHARSET=utf8mb4": true,
		"USE `newdb`;":            true,
		"`newdb`.`t`":             true,
	}
	for sub, want := range checks {
		if strings.Contains(got, sub) != want {
			t.Errorf("contains(%q) = %v, want %v\n---\n%s", sub, !want, want, got)
		}
	}
	if sum.Replacements == 0 {
		t.Error("expected some replacements")
	}
}

func TestProjectCSV(t *testing.T) {
	csvData := "a,b,c\n1,2,3\n4,5,6\n"
	var out strings.Builder
	rows, err := ProjectCSV(strings.NewReader(csvData), &out, ',', []string{"c", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want 2", rows)
	}
	want := "c,a\n3,1\n6,4\n"
	if out.String() != want {
		t.Fatalf("projected wrong:\n%q\nwant\n%q", out.String(), want)
	}
	// Unknown column → error.
	if _, err := ProjectCSV(strings.NewReader(csvData), &strings.Builder{}, ',', []string{"nope"}); err == nil {
		t.Error("expected error for unknown column")
	}
}

func TestAddCSVColumn(t *testing.T) {
	csvData := "a,b\n1,2\n"
	var out strings.Builder
	if _, err := AddCSVColumn(strings.NewReader(csvData), &out, ',', "src", "import"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a,b,src\n1,2,import\n" {
		t.Fatalf("add-column wrong:\n%q", out.String())
	}
}
