package extract

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"novera/internal/bigfile/plugins/sql/analyze"
)

func TestUnsafeClientBoundariesCannotPublishExtractionOutput(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   error
	}{
		{
			name:   "Unicode PostgreSQL dollar body",
			source: "DO $\u00e9$ BEGIN NULL; INSERT INTO phantom VALUES (1); END; $\u00e9$;",
			want:   analyze.ErrUnsupportedCompoundStatement,
		},
		{
			name: "SQL Server GO",
			source: "SET NOCOUNT ON\nGO\nCREATE TRIGGER trg ON src AFTER INSERT AS BEGIN NULL; " +
				"INSERT INTO phantom VALUES (1); END;",
			want: analyze.ErrUnsupportedCompoundStatement,
		},
		{
			name: "Firebird terminator",
			source: "SET TERM ^ ;\nEXECUTE BLOCK AS BEGIN x=1; " +
				"INSERT INTO phantom VALUES (1); END^\nSET TERM ; ^",
			want: analyze.ErrUnsupportedDelimiter,
		},
		{
			name: "SQL Plus commands",
			source: "PROMPT loading\nBEGIN NULL; " +
				"INSERT INTO phantom VALUES (1); END;\n/",
			want: analyze.ErrUnsupportedCompoundStatement,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := []byte("CREATE TABLE retained (id int);\nINSERT INTO retained VALUES (1);\n" + test.source)
			summary, err := analyze.Analyze(context.Background(), bytes.NewReader(data), analyze.Options{ChunkSize: 3})
			if !errors.Is(err, test.want) {
				t.Fatalf("Analyze error = %v, want %v", err, test.want)
			}
			if !reflect.DeepEqual(summary, analyze.Summary{}) {
				t.Fatalf("unsafe analysis returned extraction metadata: %#v", summary)
			}

			outDir := filepath.Join(t.TempDir(), "must-not-exist")
			writeSummary, writeErr := SplitByTable(
				context.Background(), bytes.NewReader(data), filepath.Join(t.TempDir(), "unsafe.sql"), summary,
				WriteOptions{OutputDir: outDir},
			)
			if writeErr == nil {
				t.Fatal("empty fail-closed analysis unexpectedly started extraction")
			}
			if !reflect.DeepEqual(writeSummary, WriteSummary{}) {
				t.Fatalf("failed extraction returned partial publication summary: %#v", writeSummary)
			}
			if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
				t.Fatalf("failed extraction created output path: %v", statErr)
			}
		})
	}
}
