package datatools

import (
	"context"
	"errors"
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

func TestTransformDumpProtectsCommentsAndStringLiterals(t *testing.T) {
	dump := "-- ENGINE=MyISAM USE `olddb`;\n" +
		"/* CHARSET=latin1\nCOLLATE old_collation */\n" +
		"CREATE TABLE `t` (`ENGINE=MyISAM` TEXT, note TEXT DEFAULT 'ENGINE=MyISAM USE `olddb`') ENGINE=MyISAM DEFAULT CHARSET=latin1 COLLATE=old_collation;\n" +
		"INSERT INTO `olddb`.`t` VALUES ('AUTO_INCREMENT=4 ENGINE=MyISAM');\n" +
		"INSERT INTO `olddb`.`t` VALUES ('multiline\nENGINE=MyISAM CHARSET=latin1');\n"
	var out strings.Builder
	_, err := TransformDump(strings.NewReader(dump), &out, DumpTransform{
		DropAutoIncrement: true,
		Engine:            "InnoDB",
		Charset:           "utf8mb4",
		Collation:         "utf8mb4_bin",
		FromDatabase:      "olddb",
		ToDatabase:        "newdb",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, preserved := range []string{
		"-- ENGINE=MyISAM USE `olddb`;",
		"/* CHARSET=latin1\nCOLLATE old_collation */",
		"'ENGINE=MyISAM USE `olddb`'",
		"`ENGINE=MyISAM` TEXT",
		"'AUTO_INCREMENT=4 ENGINE=MyISAM'",
		"'multiline\nENGINE=MyISAM CHARSET=latin1'",
	} {
		if !strings.Contains(got, preserved) {
			t.Errorf("protected SQL data changed; missing %q in:\n%s", preserved, got)
		}
	}
	for _, changed := range []string{"ENGINE=InnoDB", "DEFAULT CHARSET=utf8mb4", "COLLATE=utf8mb4_bin", "`newdb`.`t`"} {
		if !strings.Contains(got, changed) {
			t.Errorf("expected code replacement %q in:\n%s", changed, got)
		}
	}
}

func TestTransformDumpRejectsReplacementSyntaxAndInvalidPair(t *testing.T) {
	for _, transform := range []DumpTransform{
		{Engine: "$1; DROP TABLE users"},
		{Charset: "utf8 mb4"},
		{Collation: "utf8mb4;--"},
		{FromDatabase: "old"},
		{ToDatabase: "new"},
	} {
		var out strings.Builder
		if _, err := TransformDump(strings.NewReader("CREATE TABLE t (id INT);\n"), &out, transform); err == nil {
			t.Fatalf("TransformDump(%+v) succeeded; want validation error", transform)
		}
		if out.Len() != 0 {
			t.Fatalf("invalid transform wrote %d bytes before rejection", out.Len())
		}
	}
}

func TestTransformDumpEnforcesLogicalLineBudget(t *testing.T) {
	line := strings.Repeat("x", maxSQLLogicalLineBytes+1)
	var out strings.Builder
	_, err := TransformDump(strings.NewReader(line), &out, DumpTransform{Engine: "InnoDB"})
	if !errors.Is(err, ErrSQLLogicalLineTooLong) {
		t.Fatalf("error = %v, want ErrSQLLogicalLineTooLong", err)
	}
	if out.Len() != 0 {
		t.Fatalf("oversized line wrote %d bytes", out.Len())
	}
}

func TestTransformDumpHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out strings.Builder
	_, err := TransformDumpContext(ctx, strings.NewReader("CREATE TABLE t (id INT);\n"), &out, DumpTransform{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if out.Len() != 0 {
		t.Fatalf("canceled transform wrote %d bytes", out.Len())
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

func TestAddCSVColumnToleratesRaggedRows(t *testing.T) {
	csvData := "a,b\n1\n2,3,4\n"
	var out strings.Builder
	rows, err := AddCSVColumn(strings.NewReader(csvData), &out, ',', "src", "import")
	if err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want 2", rows)
	}
	want := "a,b,src\n1,import\n2,3,4,import\n"
	if out.String() != want {
		t.Fatalf("add-column ragged wrong:\n got %q\nwant %q", out.String(), want)
	}
}
