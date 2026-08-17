package analyze

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeRejectsCustomDelimiterRoutineBodiesWithoutPartialSummary(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		chunkSize int
	}{
		{
			name: "BOM deeply indented procedure across byte seams and CRLF",
			source: "\xef\xbb\xbf" + strings.Repeat(" ", 97) + "DeLiMiTeR $$\r\n" +
				"CREATE PROCEDURE p()\r\nBEGIN\r\n" +
				"  INSERT INTO procedure_phantom VALUES (1);\r\nEND$$\r\n" +
				"DELIMITER ;\r\nCREATE TABLE after_procedure (id int);\r\n",
			chunkSize: 1,
		},
		{
			name: "tab-indented trigger with insert split across chunks",
			source: "CREATE TABLE before_trigger (id int);\n" + strings.Repeat("\t", 41) + "DELIMITER //\n" +
				"CREATE TRIGGER audit AFTER INSERT ON before_trigger FOR EACH ROW\nBEGIN\n" +
				"  INSERT INTO trigger_phantom VALUES (NEW.id);\nEND//\nDELIMITER ;\n",
			chunkSize: 7,
		},
		{
			name:      "directive keyword ending exactly at EOF",
			source:    "CREATE TABLE partial (id int);\n" + strings.Repeat(" ", 64) + "delimiter",
			chunkSize: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, err := Analyze(context.Background(), memReader{data: []byte(tt.source)}, Options{ChunkSize: tt.chunkSize})
			if !errors.Is(err, ErrUnsupportedDelimiter) {
				t.Fatalf("Analyze error = %v, want ErrUnsupportedDelimiter", err)
			}
			if !reflect.DeepEqual(summary, Summary{}) {
				t.Fatalf("unsupported routine returned partial summary: %#v", summary)
			}
		})
	}
}

func TestAnalyzeDoesNotTreatDelimiterIdentifierTextAsDirective(t *testing.T) {
	source := "CREATE TABLE delimiter_settings (delimiter_value varchar(32));\n" +
		"INSERT INTO delimiter_settings VALUES ('DELIMITER $$');\n"
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != "delimiter_settings" {
		t.Fatalf("ordinary delimiter identifier text changed analysis: %#v", summary.Tables)
	}
}
