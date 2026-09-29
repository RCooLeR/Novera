package reshape

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomDelimiterRoutineFailsWithoutOutput(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{
			name: "routine body",
			sql: "DELIMITER $$\nCREATE PROCEDURE p()\nBEGIN\n" +
				"  INSERT INTO t VALUES (1);\nEND$$\nDELIMITER ;\n",
		},
		{
			name: "BOM indentation and CRLF",
			sql: "\xef\xbb\xbf" + strings.Repeat("\t", 40) + "DeLiMiTeR //\r\n" +
				"CREATE TRIGGER p BEFORE INSERT ON t FOR EACH ROW\r\nBEGIN\r\n" +
				"  INSERT INTO audit VALUES (NEW.id);\r\nEND//\r\n",
		},
		{
			name: "keyword exactly at EOF",
			sql:  "INSERT INTO t VALUES (1),(2);\n" + strings.Repeat(" ", 96) + "delimiter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "input.sql")
			dst := filepath.Join(dir, "output.sql")
			if err := os.WriteFile(src, []byte(tt.sql), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := ReshapeInsertsFile(context.Background(), src, dst, Options{Mode: ModeSingleRow})
			if !errors.Is(err, ErrUnsupportedDelimiter) {
				t.Fatalf("ReshapeInsertsFile error = %v, want ErrUnsupportedDelimiter", err)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed reshape published output: %v", statErr)
			}
		})
	}
}

func TestDelimiterIdentifierTextStillReshapes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "input.sql")
	dst := filepath.Join(dir, "output.sql")
	sql := "CREATE TABLE delimiter_settings (delimiter_value text);\n" +
		"INSERT INTO delimiter_settings VALUES ('DELIMITER $$'),('ordinary');\n"
	if err := os.WriteFile(src, []byte(sql), 0o600); err != nil {
		t.Fatal(err)
	}

	summary, err := ReshapeInsertsFile(context.Background(), src, dst, Options{Mode: ModeSingleRow})
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowsSeen != 2 || summary.InsertsRewritten != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}
