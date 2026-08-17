package analyze

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const clientSafetySeamChunk = 17

// seamedHazardSource places the first two bytes of hazard on opposite sides
// of a committed analyzer state boundary. With 17-byte reads, the first carry
// commit ends at byte 4 and later commits advance by 17 bytes.
func seamedHazardSource(prefix, beforeHazard, hazard string) string {
	paddingBytes := analyzerCarrySize + clientSafetySeamChunk
	for (len(prefix)+paddingBytes+len(beforeHazard))%clientSafetySeamChunk != 3 {
		paddingBytes++
	}
	return prefix + strings.Repeat(" ", paddingBytes) + beforeHazard + hazard
}

func TestAnalyzeReproducedPhantomRegionsFailClosedAcrossCommittedSeams(t *testing.T) {
	prefix := "CREATE TABLE retained (id int);\nINSERT INTO retained VALUES (1);\n"
	tests := []struct {
		name         string
		beforeHazard string
		hazard       string
		want         error
	}{
		{
			name:   "PostgreSQL Unicode dollar body",
			hazard: "DO $\u00e9$ BEGIN NULL; INSERT INTO phantom VALUES (1); END; $\u00e9$;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:         "SQL Server GO before trigger",
			beforeHazard: "SET NOCOUNT ON\n",
			hazard: "GO\nCREATE TRIGGER trg ON src AFTER INSERT AS BEGIN NULL; " +
				"INSERT INTO phantom VALUES (1); END;",
			want: ErrUnsupportedCompoundStatement,
		},
		{
			name: "Firebird SET TERM and EXECUTE BLOCK",
			hazard: "SET TERM ^ ;\nEXECUTE BLOCK AS BEGIN x=1; " +
				"INSERT INTO phantom VALUES (1); END^\nSET TERM ; ^",
			want: ErrUnsupportedDelimiter,
		},
		{
			name: "SQL Plus PROMPT and slash",
			hazard: "PROMPT loading\nBEGIN NULL; " +
				"INSERT INTO phantom VALUES (1); END;\n/",
			want: ErrUnsupportedCompoundStatement,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := seamedHazardSource(prefix, test.beforeHazard, test.hazard)
			summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: clientSafetySeamChunk})
			if !errors.Is(err, test.want) {
				t.Fatalf("Analyze error = %v, want %v", err, test.want)
			}
			if !reflect.DeepEqual(summary, Summary{}) {
				t.Fatalf("unsafe analysis exposed partial extraction regions: %#v", summary)
			}
		})
	}
}

func TestAnalyzeCompoundAliasesAndOracleLabelFailClosedAcrossCommittedSeams(t *testing.T) {
	prefix := "CREATE TABLE retained (id int);\nINSERT INTO retained VALUES (1);\n"
	tests := []struct {
		name   string
		hazard string
		want   error
	}{
		{
			name:   "CREATE PROC",
			hazard: "CREATE PROC p AS BEGIN SELECT 1; INSERT INTO phantom VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "ALTER PROC",
			hazard: "ALTER PROC p AS BEGIN SELECT 1; INSERT INTO phantom VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "REPLACE PROCEDURE",
			hazard: "REPLACE PROCEDURE p() BEGIN SELECT 1; INSERT INTO phantom VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "CREATE MACRO",
			hazard: "CREATE MACRO m AS (SELECT 1; INSERT INTO phantom VALUES (1););",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "plain DO block",
			hazard: "DO BEGIN SELECT 1; INSERT INTO phantom VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "Oracle block label",
			hazard: "<<load_rows>>\nBEGIN NULL; INSERT INTO phantom VALUES (1); END;",
			want:   ErrUnsupportedLexicalConstruct,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := seamedHazardSource(prefix, "", test.hazard)
			summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: clientSafetySeamChunk})
			if !errors.Is(err, test.want) {
				t.Fatalf("Analyze error = %v, want %v", err, test.want)
			}
			if !reflect.DeepEqual(summary, Summary{}) {
				t.Fatalf("unsafe alias exposed partial extraction regions: %#v", summary)
			}
		})
	}
}

func TestDollarQuoteDetectorSupportsUnicodeTagsAndUnicodeIdentifierBoundaries(t *testing.T) {
	for _, input := range []string{
		"$\u00e9$",
		"$tag\u00e9$",
		"$\u0442\u0435\u0433_9$",
	} {
		var detector dollarQuoteDetector
		found := false
		for _, c := range []byte(input) {
			if detector.Step(c) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Unicode dollar quote %q was not rejected", input)
		}
	}

	for _, input := range []string{
		"caf\u00e9$tag$",
		"name$tag$",
		"$9not_a_tag$",
	} {
		var detector dollarQuoteDetector
		for index, c := range []byte(input) {
			if detector.Step(c) {
				t.Fatalf("identifier/non-tag %q rejected at byte %d", input, index)
			}
		}
	}
}

func TestAnalyzeClientNearMissesRetainExactExtractionRegions(t *testing.T) {
	createStatement := "CREATE TABLE retained (id int, payload text);"
	insertStatement := "INSERT INTO retained VALUES (1, 'GO PROMPT REM REMARK SET TERM / \\copy $\u00e9$');"
	replaceStatement := "REPLACE INTO procedure VALUES (2);"
	source := createStatement + "\n" + insertStatement + "\n" +
		"-- GO 4\n/* PROMPT ignored; SET TERM ignored */\n" +
		"GO_TO value;\nPROMPTLY value;\nREM_VALUE value;\nREMARKABLE value;\n" +
		"SET TERMINAL = 1;\nEXECUTE STATEMENT 'not a block';\n" +
		"SELECT 8\n / 2;\nSELECT caf\u00e9$tag$ FROM retained;\n" +
		replaceStatement

	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	names := tableNames(summary)
	retained, ok := names["retained"]
	if !ok || len(retained.Regions) != 2 {
		t.Fatalf("retained table = %#v, present=%v", retained, ok)
	}
	procedure, ok := names["procedure"]
	if !ok || len(procedure.Regions) != 1 || procedure.Regions[0].Kind != RegionReplace {
		t.Fatalf("REPLACE target = %#v, present=%v", procedure, ok)
	}

	wantRetained := []string{createStatement, insertStatement}
	for index, region := range retained.Regions {
		if got := source[region.StartOffset:region.EndOffset]; got != wantRetained[index] {
			t.Fatalf("retained region %d = %q, want %q", index, got, wantRetained[index])
		}
	}
	region := procedure.Regions[0]
	if got := source[region.StartOffset:region.EndOffset]; got != replaceStatement {
		t.Fatalf("REPLACE region = %q, want %q", got, replaceStatement)
	}
}
