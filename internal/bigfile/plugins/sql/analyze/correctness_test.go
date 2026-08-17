package analyze

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeRecognizesMySQLInsertModifiersAndReplace(t *testing.T) {
	statements := []struct {
		prefix string
		name   string
		kind   RegionKind
	}{
		{"INSERT INTO", "plain", RegionInsert},
		{"INSERT LOW_PRIORITY INTO", "low", RegionInsert},
		{"INSERT DELAYED INTO", "delayed", RegionInsert},
		{"INSERT HIGH_PRIORITY INTO", "high", RegionInsert},
		{"INSERT IGNORE INTO", "ignored", RegionInsert},
		{"INSERT HIGH_PRIORITY IGNORE INTO", "high_ignored", RegionInsert},
		{"REPLACE INTO", "replaced", RegionReplace},
		{"REPLACE LOW_PRIORITY INTO", "replace_low", RegionReplace},
		{"REPLACE DELAYED INTO", "replace_delayed", RegionReplace},
	}
	var source strings.Builder
	for _, statement := range statements {
		source.WriteString(statement.prefix)
		source.WriteString(" `")
		source.WriteString(statement.name)
		source.WriteString("` VALUES (1);\n")
	}

	summary, err := Analyze(context.Background(), memReader{data: []byte(source.String())}, Options{ChunkSize: 17})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != len(statements) || summary.InsertTables != len(statements) {
		t.Fatalf("tables=%d insert tables=%d, want %d: %#v", len(summary.Tables), summary.InsertTables, len(statements), summary.Tables)
	}
	byName := tableNames(summary)
	for _, statement := range statements {
		table, ok := byName[statement.name]
		if !ok {
			t.Fatalf("missing %s target %q: %#v", statement.prefix, statement.name, summary.Tables)
		}
		if len(table.Regions) != 1 || table.Regions[0].Kind != statement.kind {
			t.Fatalf("regions for %q = %#v, want one %q", statement.name, table.Regions, statement.kind)
		}
	}
}

func TestAnalyzeRequiresKeywordBoundaries(t *testing.T) {
	source := strings.Join([]string{
		"preINSERT INTO phantom_insert VALUES (1);",
		"INSERTED INTO phantom_inserted VALUES (1);",
		"preREPLACE INTO phantom_replace VALUES (1);",
		"REPLACEMENT INTO phantom_replacement VALUES (1);",
		"preCREATE TABLE phantom_create (id int);",
		"CREATE TABLE real_table (id int);",
	}, "\n")

	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 19})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != "real_table" {
		t.Fatalf("keyword substring produced phantom metadata: %#v", summary.Tables)
	}
}

func TestAnalyzeRetainsQualifiedIdentityAndInterleavedRegions(t *testing.T) {
	source := strings.Join([]string{
		"CREATE TABLE `db1`.`users` (id int);",
		"CREATE TABLE `db2`.`users` (id int);",
		"INSERT INTO `db1`.`users` VALUES (1);",
		"INSERT INTO `db2`.`users` VALUES (2);",
		"REPLACE INTO `db1`.`users` VALUES (3);",
	}, "\n") + "\n"

	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 23})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 2 {
		t.Fatalf("qualified identities collapsed: %#v", summary.Tables)
	}
	byDisplay := make(map[string]Table, len(summary.Tables))
	for _, table := range summary.Tables {
		byDisplay[table.Name] = table
	}
	db1, ok := byDisplay["`db1`.`users`"]
	if !ok || db1.Database != "db1" || db1.TableName != "users" || len(db1.Regions) != 3 {
		t.Fatalf("db1 identity/regions = %#v", db1)
	}
	db2, ok := byDisplay["`db2`.`users`"]
	if !ok || db2.Database != "db2" || db2.TableName != "users" || len(db2.Regions) != 2 {
		t.Fatalf("db2 identity/regions = %#v", db2)
	}
	for _, table := range summary.Tables {
		for i, region := range table.Regions {
			if region.StartOffset < 0 || region.EndOffset <= region.StartOffset || region.EndOffset > int64(len(source)) {
				t.Fatalf("invalid region %d for %q: %#v", i, table.Name, region)
			}
			if i > 0 && table.Regions[i-1].StartOffset >= region.StartOffset {
				t.Fatalf("regions not ordered for %q: %#v", table.Name, table.Regions)
			}
		}
	}
}

func TestAnalyzeUnescapesBackticksWithoutIdentityCollision(t *testing.T) {
	source := "INSERT INTO `db``one`.`ta``ble` VALUES (1);\nINSERT INTO `db`.`one.ta``ble` VALUES (2);\n"
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 13})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 2 {
		t.Fatalf("escaped identities collapsed: %#v", summary.Tables)
	}
	want := map[string]bool{
		"`db``one`.`ta``ble`": false,
		"`db`.`one.ta``ble`":  false,
	}
	for _, table := range summary.Tables {
		if _, ok := want[table.Name]; !ok {
			t.Fatalf("unexpected escaped display identity %q", table.Name)
		}
		want[table.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Fatalf("missing escaped display identity %q: %#v", name, summary.Tables)
		}
	}
}

func TestAnalyzeMasksSQLLookingTextInsideBacktickIdentifiers(t *testing.T) {
	source := "CREATE TABLE `real` (\n" +
		"  `column INSERT INTO phantom_insert VALUES (1)` int,\n" +
		"  `column REPLACE INTO phantom_replace VALUES (2)` int,\n" +
		"  `escaped `` INSERT INTO phantom_doubled VALUES (3)` int\n" +
		");\n" +
		"INSERT INTO `real` (`payload INSERT INTO phantom_target VALUES (4)`) VALUES (1);\n"

	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 11})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != "real" {
		t.Fatalf("backtick identifier text produced phantom regions: %#v", summary.Tables)
	}
	if got := summary.Tables[0].Regions; len(got) != 2 || got[0].Kind != RegionCreate || got[1].Kind != RegionInsert {
		t.Fatalf("real table regions = %#v, want CREATE then INSERT", got)
	}
}

func TestAnalyzeBacktickDoubledEscapeAcrossCarryStateSeam(t *testing.T) {
	// With a 17-byte chunk and a 16 KiB carry, analyzer state boundaries are at
	// offsets 4, 21, 38, ... . The doubled ticks below occupy offsets 20 and 21,
	// proving that justExited is carried when the pair itself crosses the seam.
	prefix := "CREATE TABLE t (`abc"
	if len(prefix) != 20 {
		t.Fatalf("test prefix length = %d, want 20", len(prefix))
	}
	source := prefix + "`` INSERT INTO seam_phantom VALUES (1)` int);\n" +
		"INSERT INTO t VALUES (1);\n-- " + strings.Repeat("padding", analyzerCarrySize/len("padding")+8) + "\n"

	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 17})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != "t" {
		t.Fatalf("doubled-backtick seam produced phantom metadata: %#v", summary.Tables)
	}
}

func TestAnalyzeSupportsDoubleQuotedIdentifiersAndMasksTheirContents(t *testing.T) {
	source := "CREATE TABLE \"real\"\"table\" (\n" +
		"  \"column INSERT INTO phantom_insert VALUES (1)\" int,\n" +
		"  note varchar(64) DEFAULT \"value; INSERT INTO phantom_value VALUES (2)\"\n" +
		");\n" +
		"INSERT INTO \"real\"\"table\" (\"payload INSERT INTO phantom_target VALUES (3)\") VALUES (1);\n"

	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != `real"table` {
		t.Fatalf("double-quoted identifier analysis = %#v", summary.Tables)
	}
	if got := summary.Tables[0].Regions; len(got) != 2 || got[0].Kind != RegionCreate || got[1].Kind != RegionInsert {
		t.Fatalf("double-quoted regions = %#v, want CREATE then INSERT", got)
	}
}

func TestAnalyzeDoubleQuoteEscapeAcrossCarryStateSeam(t *testing.T) {
	// With 17-byte reads, committed state boundaries are 4, 21, 38, ... after
	// the initial 16 KiB carry fills. Put the doubled quotes at offsets 20/21.
	prefix := `CREATE TABLE "abcxxx`
	if len(prefix) != 20 {
		t.Fatalf("test prefix length = %d, want 20", len(prefix))
	}
	wantName := `abcxxx" INSERT INTO seam_phantom VALUES (1)`
	source := prefix + `"" INSERT INTO seam_phantom VALUES (1)" (id int);` +
		"\n-- " + strings.Repeat("padding", analyzerCarrySize/len("padding")+8) + "\n"

	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 17})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != wantName {
		t.Fatalf("doubled-double-quote seam analysis = %#v, want %q", summary.Tables, wantName)
	}
}

func TestAnalyzeRegionsEndAtTheirOwnTopLevelSemicolon(t *testing.T) {
	source := "SET @header = 1;\n" +
		"CREATE TABLE `target` (`semi;column` varchar(64), note varchar(64) DEFAULT 'text;still literal');\n" +
		"UPDATE unrelated SET value = 'not target;';\n" +
		"INSERT INTO `target` VALUES (1, 'insert;literal') /* comment ; */;\n" +
		"DELETE FROM unrelated WHERE id = 1;\n" +
		"SET @footer = 'footer;not target';\n"
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 13})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != "target" {
		t.Fatalf("tables = %#v, want target only", summary.Tables)
	}
	regions := summary.Tables[0].Regions
	if len(regions) != 2 {
		t.Fatalf("regions = %#v, want CREATE and INSERT", regions)
	}
	want := []string{
		"CREATE TABLE `target` (`semi;column` varchar(64), note varchar(64) DEFAULT 'text;still literal');",
		"INSERT INTO `target` VALUES (1, 'insert;literal') /* comment ; */;",
	}
	for i, region := range regions {
		if got := source[region.StartOffset:region.EndOffset]; got != want[i] {
			t.Fatalf("region %d bytes = %q, want exact %q", i, got, want[i])
		}
	}
}

func TestAnalyzeRegionTerminatorCrossesCarryStateSeam(t *testing.T) {
	prefix := "CREATE TABLE seam_table (id int /*"
	suffix := "*/"
	// Chunk 17 with a 16 KiB carry first commits four bytes, then advances in
	// 17-byte steps. Place the statement semicolon on one such boundary and put
	// more than a carry of footer bytes after it so the boundary is exercised
	// during streaming rather than only by the final carry flush.
	padding := 128
	for (len(prefix)+padding+len(suffix))%17 != 4 {
		padding++
	}
	statement := prefix + strings.Repeat("x", padding) + suffix + ";"
	source := statement + "\n-- " + strings.Repeat("footer", analyzerCarrySize/len("footer")+16)
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 17})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || len(summary.Tables[0].Regions) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	region := summary.Tables[0].Regions[0]
	if region.EndOffset != int64(len(statement)) || source[region.StartOffset:region.EndOffset] != statement {
		t.Fatalf("seam region = %#v, bytes %q, want exact statement length %d", region, source[region.StartOffset:region.EndOffset], len(statement))
	}
}

func TestAnalyzeRecognizedStatementEOFPolicyFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		source string
		wantOK bool
	}{
		{name: "terminator is final byte", source: "INSERT INTO final_byte VALUES ('inside;literal');", wantOK: true},
		{name: "missing terminator", source: "INSERT INTO unterminated VALUES (1)"},
		{name: "literal semicolon is not terminator", source: "INSERT INTO literal_only VALUES (';')"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, err := Analyze(context.Background(), memReader{data: []byte(tt.source)}, Options{ChunkSize: 5})
			if tt.wantOK {
				if err != nil {
					t.Fatal(err)
				}
				if len(summary.Tables) != 1 || summary.Tables[0].Regions[0].EndOffset != int64(len(tt.source)) {
					t.Fatalf("summary = %#v, want exact EOF terminator", summary)
				}
				return
			}
			if !errors.Is(err, ErrUnterminatedStatement) {
				t.Fatalf("Analyze error = %v, want ErrUnterminatedStatement", err)
			}
			if len(summary.Tables) != 0 {
				t.Fatalf("unterminated analysis returned partial tables: %#v", summary.Tables)
			}
		})
	}
}

func TestAnalyzeRejectsNestedRecognizedStartBeforeTerminator(t *testing.T) {
	source := "INSERT INTO outer_table VALUES (REPLACE INTO nested_table VALUES (1));"
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 9})
	if !errors.Is(err, ErrAmbiguousStatement) {
		t.Fatalf("Analyze error = %v, want ErrAmbiguousStatement", err)
	}
	if len(summary.Tables) != 0 {
		t.Fatalf("ambiguous analysis returned partial tables: %#v", summary.Tables)
	}
}

func TestAnalyzeRejectsDataKeywordsThatDoNotOwnStatementPrefix(t *testing.T) {
	for _, source := range []string{
		"WITH c AS (SELECT 1) INSERT INTO cte_target SELECT * FROM c;",
		"EXPLAIN INSERT INTO explained VALUES (1);",
		"EXPLAIN ANALYZE INSERT INTO explained VALUES (1);",
	} {
		summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 7})
		if !errors.Is(err, ErrAmbiguousStatement) {
			t.Fatalf("Analyze(%q) error = %v, want ErrAmbiguousStatement", source, err)
		}
		if !reflect.DeepEqual(summary, Summary{}) {
			t.Fatalf("wrapped data statement returned partial summary: %#v", summary)
		}
	}
}

func TestAnalyzeRejectsUnsupportedCompoundBodiesBeforeInnerStatements(t *testing.T) {
	prefix := "CREATE TABLE retained (id int);\nINSERT INTO retained VALUES (1);\n"
	tests := []struct {
		name   string
		source string
	}{
		{
			name:   "single statement SQLite trigger",
			source: "CREATE TRIGGER trg AFTER INSERT ON src FOR EACH ROW INSERT INTO audit VALUES (NEW.id);",
		},
		{
			name: "multi statement SQLite trigger",
			source: "CREATE TRIGGER trg AFTER INSERT ON src BEGIN\n" +
				" INSERT INTO audit VALUES (1);\n INSERT INTO audit2 VALUES (2);\nEND;",
		},
		{
			name: "MySQL procedure without client directive",
			source: "CREATE DEFINER=`root`@`localhost` PROCEDURE p() BEGIN\n" +
				" INSERT INTO audit VALUES (1);\n INSERT INTO audit2 VALUES (2);\nEND;",
		},
		{
			name:   "MySQL unquoted definer procedure",
			source: "CREATE DEFINER=user@host PROCEDURE p() BEGIN SELECT 1; INSERT INTO audit VALUES (1); END;",
		},
		{
			name:   "MySQL unquoted definer trigger",
			source: "CREATE DEFINER=user@host TRIGGER trg BEFORE INSERT ON src FOR EACH ROW BEGIN INSERT INTO audit VALUES (1); END;",
		},
		{
			name:   "MySQL unquoted definer function",
			source: "CREATE DEFINER=user@host FUNCTION f() RETURNS INT BEGIN INSERT INTO audit VALUES (1); RETURN 1; END;",
		},
		{
			name:   "MySQL unquoted definer event",
			source: "CREATE DEFINER=user@host EVENT e DO BEGIN INSERT INTO audit VALUES (1); END;",
		},
		{
			name: "PostgreSQL dollar quoted function",
			source: "CREATE OR REPLACE FUNCTION f() RETURNS void AS $body$ BEGIN; " +
				"INSERT INTO audit VALUES (1); END; $body$ LANGUAGE plpgsql;",
		},
		{
			name:   "PostgreSQL anonymous dollar quoted block",
			source: "DO $$ BEGIN; INSERT INTO audit VALUES (1); END; $$;",
		},
		{
			name:   "PostgreSQL Unicode dollar quoted block",
			source: "DO $тег$ BEGIN; INSERT INTO audit VALUES (1); END; $тег$;",
		},
		{
			name:   "PostgreSQL rule",
			source: "CREATE RULE r AS ON INSERT TO src DO ALSO INSERT INTO audit VALUES (NEW.id);",
		},
		{
			name: "SQL Server ALTER trigger with repeated inner inserts",
			source: "ALTER TRIGGER trg ON src AFTER INSERT AS BEGIN\n" +
				" INSERT INTO audit VALUES (1);\n INSERT INTO audit2 VALUES (2);\nEND;",
		},
		{
			name: "SQL Server CREATE OR ALTER procedure",
			source: "CREATE OR ALTER PROCEDURE p AS BEGIN\n" +
				" INSERT INTO audit VALUES (1);\n INSERT INTO audit2 VALUES (2);\nEND;",
		},
		{
			name:   "SQL Server CREATE PROC alias",
			source: "CREATE PROC p AS BEGIN SET NOCOUNT ON; INSERT INTO audit VALUES (1); END;",
		},
		{
			name:   "HANA DO block",
			source: "DO BEGIN DECLARE n INTEGER; INSERT INTO audit VALUES (1); END;",
		},
		{
			name:   "Firebird EXECUTE BLOCK",
			source: "EXECUTE BLOCK AS BEGIN POST_EVENT 'x'; INSERT INTO audit VALUES (1); END;",
		},
		{
			name:   "Firebird RECREATE PROCEDURE",
			source: "RECREATE PROCEDURE p AS BEGIN INSERT INTO audit VALUES (1); END;",
		},
		{
			name:   "Teradata REPLACE PROCEDURE",
			source: "REPLACE PROCEDURE p() BEGIN INSERT INTO audit VALUES (1); END;",
		},
		{
			name:   "compound macro",
			source: "CREATE MACRO m AS (INSERT INTO audit VALUES (1););",
		},
		{
			name:   "Snowflake CREATE TASK scripting block",
			source: "CREATE TASK load_rows AS BEGIN SELECT 1; INSERT INTO audit VALUES (1); END;",
		},
		{
			name: "Oracle CREATE TYPE BODY",
			source: "CREATE OR REPLACE TYPE BODY widget AS\n" +
				" MEMBER PROCEDURE write_audit IS BEGIN INSERT INTO audit VALUES (1); END;\nEND;",
		},
		{
			name: "Oracle CREATE PACKAGE BODY",
			source: "CREATE OR REPLACE PACKAGE BODY pkg AS\n" +
				" PROCEDURE p IS BEGIN INSERT INTO audit VALUES (1); END;\nEND;",
		},
		{
			name:   "Oracle anonymous BEGIN block",
			source: "BEGIN\n INSERT INTO audit VALUES (1);\n INSERT INTO audit2 VALUES (2);\nEND;",
		},
		{
			name:   "Oracle anonymous DECLARE block",
			source: "DECLARE value NUMBER := 1; BEGIN INSERT INTO audit VALUES (value); END;",
		},
		{
			name:   "standalone procedural IF",
			source: "IF condition THEN INSERT INTO audit VALUES (1); END IF;",
		},
		{
			name: "PostgreSQL COPY payload",
			source: "COPY imported FROM STDIN;\n" +
				"1\tINSERT INTO phantom VALUES (1);\n\\.\nCREATE TABLE retained (id int);",
		},
	}
	for _, chunkSize := range []int{1, 3} {
		for _, tt := range tests {
			t.Run(tt.name+"/chunk-"+string(rune('0'+chunkSize)), func(t *testing.T) {
				summary, err := Analyze(context.Background(), memReader{data: []byte(prefix + tt.source)}, Options{ChunkSize: chunkSize})
				if !errors.Is(err, ErrUnsupportedCompoundStatement) {
					t.Fatalf("Analyze error = %v, want ErrUnsupportedCompoundStatement", err)
				}
				if !reflect.DeepEqual(summary, Summary{}) {
					t.Fatalf("unsupported compound analysis returned partial summary: %#v", summary)
				}
			})
		}
	}
}

func TestAnalyzeRejectsClientBoundaryHazardsWithoutPartialSummary(t *testing.T) {
	prefix := "CREATE TABLE retained (id int);\nINSERT INTO retained VALUES (1);\n"
	tests := []struct {
		name   string
		source string
		want   error
	}{
		{
			name:   "SQL Server GO with comment",
			source: "SET NOCOUNT ON\nGO -- next batch\nCREATE PROC p AS BEGIN SET NOCOUNT ON; INSERT INTO audit VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQL Server GO count",
			source: "SET NOCOUNT ON\nGO 2 -- repeat\nCREATE PROC p AS BEGIN INSERT INTO audit VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQL Plus slash",
			source: "SET DEFINE OFF\n/\nBEGIN INSERT INTO audit VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "psql meta command",
			source: "\\copy imported FROM STDIN\nINSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQLCMD colon command",
			source: ":r nested.sql\nINSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQLite dot command",
			source: ".read nested.sql\nINSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQLCMD shell command",
			source: "!! echo unsafe\nINSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SnowSQL bang command",
			source: "!source nested.sql\nCREATE TASK load AS BEGIN SELECT 1; INSERT INTO audit VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQL Plus PROMPT",
			source: "PROMPT heading; INSERT INTO phantom VALUES (8);\nBEGIN INSERT INTO audit VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQL Plus REM",
			source: "REM heading; INSERT INTO phantom VALUES (8);\nBEGIN INSERT INTO audit VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQL Plus REMARK",
			source: "REMARK heading; INSERT INTO phantom VALUES (8);\nBEGIN INSERT INTO audit VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "Firebird SET TERM",
			source: "SET TERM ^ ;\nEXECUTE BLOCK AS BEGIN INSERT INTO audit VALUES (1); END^\nSET TERM ; ^",
			want:   ErrUnsupportedDelimiter,
		},
		{
			name:   "unknown newline client command",
			source: "SPOOL output.log\nINSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "unknown newline client command before compound DDL",
			source: "SPOOL output.log\nCREATE TASK load AS BEGIN SELECT 1; INSERT INTO audit VALUES (1); END;",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQL Plus SPOOL same line",
			source: "SPOOL output.log; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "mysql SOURCE same line",
			source: "SOURCE nested.sql; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQL Plus at script same line",
			source: "@nested.sql; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "mysql help shorthand same line",
			source: "? contents; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "unknown client command same line",
			source: "VENDORCMD output.log; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "leading comment before unknown client command",
			source: "/**/ VENDORCMD output.log; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "SQL Plus WHENEVER same line",
			source: "WHENEVER SQLERROR CONTINUE; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name: "file and shell client commands",
			source: "HOST echo setup; INSERT INTO audit VALUES (1);\n" +
				"SYSTEM echo setup; TEE output.log; PAGER less; INPUT nested.sql; OUTPUT output.log;",
			want: ErrUnsupportedCompoundStatement,
		},
		{
			name:   "unknown client command sticky across harmless segment",
			source: "VENDORCMD output.log; SELECT 1; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "ambiguous SET client overlap same line",
			source: "SET NAMES utf8mb4; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
		{
			name:   "ambiguous SQL Plus SHUTDOWN overlap same line",
			source: "SHUTDOWN; INSERT INTO audit VALUES (1);",
			want:   ErrUnsupportedCompoundStatement,
		},
	}
	for _, chunkSize := range []int{1, 7} {
		for _, test := range tests {
			t.Run(test.name+"/chunk-"+string(rune('0'+chunkSize)), func(t *testing.T) {
				summary, err := Analyze(context.Background(), memReader{data: []byte(prefix + test.source)}, Options{ChunkSize: chunkSize})
				if !errors.Is(err, test.want) {
					t.Fatalf("Analyze error = %v, want %v", err, test.want)
				}
				if !reflect.DeepEqual(summary, Summary{}) {
					t.Fatalf("hazard analysis returned partial summary: %#v", summary)
				}
			})
		}
	}
}

func TestAnalyzeAllowsKnownSQLSegmentsAndContinuedClientTokenNearMisses(t *testing.T) {
	createStatement := "CREATE TABLE retained (id int);"
	firstInsert := "INSERT INTO retained VALUES (1);"
	secondInsert := "INSERT INTO retained VALUES (2);"
	source := "SELECT 1; " + createStatement + " " + firstInsert + "\n" +
		"SELECT\nhost\nFROM data_source; " + secondInsert + "\n" +
		"SELECT\n@variable;"

	for _, chunkSize := range []int{1, 7} {
		summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: chunkSize})
		if err != nil {
			t.Fatalf("chunk %d: Analyze error = %v", chunkSize, err)
		}
		if len(summary.Tables) != 1 || summary.Tables[0].Name != "retained" {
			t.Fatalf("chunk %d: tables = %#v, want retained", chunkSize, summary.Tables)
		}
		regions := summary.Tables[0].Regions
		if len(regions) != 3 {
			t.Fatalf("chunk %d: regions = %#v, want create plus two inserts", chunkSize, regions)
		}
		for index, statement := range []string{createStatement, firstInsert, secondInsert} {
			region := regions[index]
			if got := source[region.StartOffset:region.EndOffset]; got != statement {
				t.Fatalf("chunk %d region %d = %q, want %q", chunkSize, index, got, statement)
			}
		}
	}
}

func TestAnalyzeAllowsSafeMultilineTableStatementPrefixes(t *testing.T) {
	createStatement := "CREATE\nTABLE multiline (id int);"
	firstInsert := "INSERT\nINTO multiline VALUES (1);"
	secondInsert := "INSERT\nIGNORE\nINTO multiline VALUES (2);"
	ignoredView := "CREATE OR\nREPLACE VIEW retained_view AS SELECT 1;"
	source := strings.Join([]string{createStatement, firstInsert, secondInsert, ignoredView}, "\n")

	for _, chunkSize := range []int{1, 5, 64} {
		summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: chunkSize})
		if err != nil {
			t.Fatalf("chunk %d: Analyze error = %v", chunkSize, err)
		}
		if len(summary.Tables) != 1 || summary.Tables[0].Name != "multiline" {
			t.Fatalf("chunk %d: tables = %#v, want only multiline", chunkSize, summary.Tables)
		}
		regions := summary.Tables[0].Regions
		if len(regions) != 3 {
			t.Fatalf("chunk %d: regions = %#v, want three exact table regions", chunkSize, regions)
		}
		wantStatements := []string{createStatement, firstInsert, secondInsert}
		for index, region := range regions {
			wantStart := int64(strings.Index(source, wantStatements[index]))
			wantEnd := wantStart + int64(len(wantStatements[index]))
			if region.StartOffset != wantStart || region.EndOffset != wantEnd ||
				source[region.StartOffset:region.EndOffset] != wantStatements[index] {
				t.Fatalf("chunk %d region %d = %#v, want exact [%d,%d) %q",
					chunkSize, index, region, wantStart, wantEnd, wantStatements[index])
			}
		}
	}
}

func TestAnalyzeMasksClientCommandsInsidePayloadAndKeepsExactRegion(t *testing.T) {
	statement := "INSERT INTO retained VALUES ('line one\nGO -- payload\nPROMPT payload; INSERT INTO phantom VALUES (9);\n/\n\\copy payload\n:r payload\n.read payload\n!! payload\n');"
	source := "SELECT\n.5;\nSELECT\n!0;\n" + statement
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || len(summary.Tables[0].Regions) != 1 {
		t.Fatalf("summary = %#v, want one exact retained region", summary)
	}
	region := summary.Tables[0].Regions[0]
	wantStart := int64(strings.Index(source, statement))
	wantEnd := wantStart + int64(len(statement))
	if region.StartOffset != wantStart || region.EndOffset != wantEnd ||
		source[region.StartOffset:region.EndOffset] != statement {
		t.Fatalf("region = %#v, want [%d,%d)", region, wantStart, wantEnd)
	}
}

func TestAnalyzeRejectsTargetPrefixesBeyondBoundedLookahead(t *testing.T) {
	startNearCommitBoundary := strings.Repeat(" ", 1016)
	longGap := strings.Repeat(" ", analyzerCarrySize+1024)
	longComment := "/*" + strings.Repeat("x", analyzerCarrySize+1024) + "*/"
	tests := []struct {
		name   string
		source string
	}{
		{name: "INSERT whitespace", source: startNearCommitBoundary + "INSERT" + longGap + "INTO far_insert VALUES (1);"},
		{name: "INSERT comment", source: startNearCommitBoundary + "INSERT " + longComment + " INTO far_comment VALUES (1);"},
		{name: "CREATE whitespace", source: startNearCommitBoundary + "CREATE" + longGap + "TABLE far_create (id int);"},
		{name: "qualified INSERT suffix", source: startNearCommitBoundary + "INSERT INTO database" + longGap + ".far_insert VALUES (1);"},
		{name: "qualified CREATE suffix", source: startNearCommitBoundary + "CREATE TABLE database" + longGap + ".far_create (id int);"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary, err := Analyze(context.Background(), memReader{data: []byte(test.source)}, Options{ChunkSize: 1024})
			if !errors.Is(err, ErrUnresolvedStatementPrefix) {
				t.Fatalf("Analyze error = %v, want ErrUnresolvedStatementPrefix", err)
			}
			if !reflect.DeepEqual(summary, Summary{}) {
				t.Fatalf("prefix overrun returned partial summary: %#v", summary)
			}
		})
	}
}

func TestAnalyzeRejectsUnsupportedStatementLeadingDataGrammar(t *testing.T) {
	prefix := "CREATE TABLE retained (id int); INSERT INTO retained VALUES (1);\n"
	for _, statement := range []string{
		"INSERT OVERWRITE TABLE target VALUES (1);",
		"INSERT target VALUES (1);",
		"INSERT UNKNOWN_MODIFIER INTO target VALUES (1);",
		"INSERT INTO TABLE target VALUES (1);",
		"INSERT INTO DIRECTORY '/tmp/output' SELECT 1;",
		"REPLACE target VALUES (1);",
		"INSERT IGNOREX INTO target VALUES (1);",
		"INSERT HIGH_PRIORITYX INTO target VALUES (1);",
		"INSERT INTO ONLY schema.target VALUES (1);",
		"CREATE TABLE ONLY (id int); INSERT INTO ONLY VALUES (1);",
		"RECREATE TABLE target (id int);",
		"CREATE OR REPLACE TABLE target (id int);",
	} {
		summary, err := Analyze(context.Background(), memReader{data: []byte(prefix + statement)}, Options{ChunkSize: 5})
		if !errors.Is(err, ErrUnresolvedStatementPrefix) {
			t.Fatalf("Analyze(%q) error = %v, want ErrUnresolvedStatementPrefix", statement, err)
		}
		if !reflect.DeepEqual(summary, Summary{}) {
			t.Fatalf("unsupported data grammar returned partial summary: %#v", summary)
		}
	}
}

func TestAnalyzeRetainsUTF8TableIdentitiesExactly(t *testing.T) {
	cafe := "caf\u00e9"
	database := "\u0431\u0430\u0437\u0430"
	users := "\u043f\u043e\u043b\u044c\u0437\u043e\u0432\u0430\u0442\u0435\u043b\u0438"
	secondDatabase := "\u0441\u0445\u0435\u043c\u0430"
	secondTable := "\u0442\u0430\u0431\u043b\u0438\u0446\u0430"
	source := "CREATE TABLE " + cafe + " (id int);\n" +
		"INSERT INTO " + cafe + " VALUES (1);\n" +
		"CREATE TABLE " + database + "." + users + " (id int);\n" +
		"INSERT INTO " + database + "." + users + " VALUES (2);\n" +
		"INSERT INTO " + secondDatabase + "." + secondTable + " VALUES (3);\n" +
		"CREATE TABLE `ONLY` (id int); INSERT INTO `ONLY` VALUES (4);\n"
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]Table, len(summary.Tables))
	for _, table := range summary.Tables {
		names[table.Database+"\x00"+table.TableName] = table
	}
	for name, regionCount := range map[string]int{
		"\x00" + cafe:                         2,
		database + "\x00" + users:             2,
		secondDatabase + "\x00" + secondTable: 1,
		"\x00ONLY":                            2,
	} {
		table, ok := names[name]
		if !ok || len(table.Regions) != regionCount {
			t.Fatalf("table %q = %#v, present=%v; all=%#v", name, table, ok, summary.Tables)
		}
		for _, region := range table.Regions {
			if region.StartOffset < 0 || region.EndOffset <= region.StartOffset ||
				!strings.HasSuffix(source[region.StartOffset:region.EndOffset], ";") {
				t.Fatalf("table %q has inexact region %#v", name, region)
			}
		}
	}
}

func TestAnalyzeRejectsInvalidUTF8IdentifierWithoutPartialSummary(t *testing.T) {
	source := append([]byte("CREATE TABLE retained (id int); INSERT INTO retained VALUES (1); INSERT INTO bad"), 0xff)
	source = append(source, []byte("name VALUES (2);")...)
	summary, err := Analyze(context.Background(), memReader{data: source}, Options{ChunkSize: 3})
	if !errors.Is(err, ErrInvalidIdentifierEncoding) || !errors.Is(err, ErrUnsupportedLexicalConstruct) {
		t.Fatalf("Analyze error = %v, want invalid UTF-8 lexical error", err)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Fatalf("invalid UTF-8 identifier returned partial summary: %#v", summary)
	}
}

func TestAnalyzeExecutableCommentPolicy(t *testing.T) {
	prefix := "CREATE TABLE retained (id int); INSERT INTO retained VALUES (1);\n"
	for _, chunkSize := range []int{1, 2} {
		for _, directive := range []string{
			"/*!50000 INSERT INTO hidden VALUES (2) */;",
			"/*M!100100 INSERT INTO hidden VALUES (2) */;",
			"/*!50000 SET @x=1; INSERT INTO hidden VALUES (2) */;",
			"/*M!100100 SET STATEMENT max_statement_time=1 FOR INSERT INTO hidden VALUES (2) */;",
			"/*+ detached_hint */ INSERT INTO hidden VALUES (2);",
		} {
			summary, err := Analyze(context.Background(), memReader{data: []byte(prefix + directive)}, Options{ChunkSize: chunkSize})
			if !errors.Is(err, ErrUnsupportedLexicalConstruct) {
				t.Fatalf("Analyze(%q, chunk %d) error = %v, want ErrUnsupportedLexicalConstruct", directive, chunkSize, err)
			}
			if !reflect.DeepEqual(summary, Summary{}) {
				t.Fatalf("unsafe executable comment returned partial summary: %#v", summary)
			}
		}
	}

	safe := "-- MySQL dump\n" +
		"/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;\n" +
		"CREATE TABLE `retained` (id int);\n" +
		"/*!40000 ALTER TABLE `retained` DISABLE KEYS */;\n" +
		"INSERT INTO `retained` VALUES (1,'/*! payload */'),(2,'/*M! payload */');\n" +
		"INSERT /*+ attached_hint */ INTO `retained` VALUES (3);\n" +
		"/*!40000 ALTER TABLE `retained` ENABLE KEYS */;\n"
	for _, chunkSize := range []int{1, 2} {
		summary, err := Analyze(context.Background(), memReader{data: []byte(safe)}, Options{ChunkSize: chunkSize})
		if err != nil {
			t.Fatalf("chunk %d: %v", chunkSize, err)
		}
		if len(summary.Tables) != 1 || summary.Tables[0].Name != "retained" || len(summary.Tables[0].Regions) != 3 {
			t.Fatalf("chunk %d: common mysqldump directives changed safe regions: %#v", chunkSize, summary)
		}
	}
}

func TestAnalyzeExecutableCommentCommittedCarrySeams(t *testing.T) {
	tail := "*/;\nCREATE TABLE seam_safe (id int);\nINSERT INTO seam_safe VALUES (1);"
	for _, test := range []struct {
		name   string
		before string
		hazard string
	}{
		{name: "opener", hazard: "/*!40101 SET @x=1 " + tail},
		{name: "SET token", before: "/*!40101 S", hazard: "ET @x=1 " + tail},
		{name: "closer", before: "/*!40101 SET @x=1 ", hazard: tail},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := seamedHazardSource("", test.before, test.hazard)
			if len(source) <= analyzerCarrySize {
				t.Fatalf("source length = %d, want a committed carry seam", len(source))
			}
			summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: clientSafetySeamChunk})
			if err != nil {
				t.Fatal(err)
			}
			if len(summary.Tables) != 1 || summary.Tables[0].Name != "seam_safe" || len(summary.Tables[0].Regions) != 2 {
				t.Fatalf("safe directive seam changed regions: %#v", summary)
			}
		})
	}

	prefix := "CREATE TABLE retained (id int); INSERT INTO retained VALUES (1);\n"
	unsafe := seamedHazardSource(prefix, "/*!50000 INSE", "RT INTO hidden VALUES (2) */;")
	summary, err := Analyze(context.Background(), memReader{data: []byte(unsafe)}, Options{ChunkSize: clientSafetySeamChunk})
	if !errors.Is(err, ErrUnsupportedLexicalConstruct) {
		t.Fatalf("hidden-DML seam error = %v, want ErrUnsupportedLexicalConstruct", err)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Fatalf("hidden-DML seam returned partial summary: %#v", summary)
	}
}

func TestAnalyzeRejectsUnsupportedLexicalConstructsWithoutPartialSummary(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name: "SQL Server bracket identifier",
			source: "CREATE TABLE [real INSERT INTO phantom VALUES (1)] (id int);\n" +
				"INSERT INTO [real INSERT INTO phantom VALUES (1)] VALUES (1);",
		},
		{
			name:   "Oracle q quote",
			source: "INSERT INTO retained VALUES (q'[Bob's; INSERT INTO phantom VALUES (1);]');",
		},
		{
			name: "dash comment without whitespace",
			source: "--comment INSERT INTO phantom VALUES (1);\n" +
				"CREATE TABLE retained (id int);",
		},
		{
			name: "ambiguous inline hash operator",
			source: "INSERT INTO target VALUES (payload #> path);\n" +
				"UPDATE unrelated SET value = 1;",
		},
		{
			name:   "Oracle block label",
			source: "<<load_rows>>\nBEGIN INSERT INTO audit VALUES (1); END;",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, err := Analyze(context.Background(), memReader{data: []byte(tt.source)}, Options{ChunkSize: 2})
			if !errors.Is(err, ErrUnsupportedLexicalConstruct) {
				t.Fatalf("Analyze error = %v, want ErrUnsupportedLexicalConstruct", err)
			}
			if len(summary.Tables) != 0 || summary.CreateTables != 0 || summary.InsertTables != 0 {
				t.Fatalf("unsupported lexical analysis returned partial summary: %#v", summary)
			}
		})
	}
}

func TestAnalyzeRejectsNestedBlockCommentAcrossCarryStateSeam(t *testing.T) {
	// Put the nested opener at offsets 20/21, across a committed lexer-state
	// boundary for 17-byte reads after the 16 KiB carry fills.
	prefix := "/* outer " + strings.Repeat("x", 11)
	if len(prefix) != 20 {
		t.Fatalf("test prefix length = %d, want 20", len(prefix))
	}
	source := prefix + "/* inner */ still-comment; INSERT INTO phantom VALUES (1); */\n" +
		strings.Repeat("padding", analyzerCarrySize/len("padding")+8)
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 17})
	if !errors.Is(err, ErrUnsupportedLexicalConstruct) {
		t.Fatalf("Analyze error = %v, want ErrUnsupportedLexicalConstruct", err)
	}
	if len(summary.Tables) != 0 || summary.CreateTables != 0 || summary.InsertTables != 0 {
		t.Fatalf("nested-comment analysis returned partial summary: %#v", summary)
	}
}

func TestAnalyzeRejectsOverlappingBlockCommentDelimitersAcrossCarryStateSeam(t *testing.T) {
	// The opening slash is offset 20 and its star is offset 21, so the
	// blockJustOpened flag must survive the committed state boundary. The star
	// in "/*/" belongs only to the opener and cannot also close the comment.
	prefix := strings.Repeat(" ", 20)
	source := prefix + "/*/ ; INSERT INTO phantom VALUES (1);\n" +
		strings.Repeat("padding", analyzerCarrySize/len("padding")+8)
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 17})
	if !errors.Is(err, ErrUnsupportedLexicalConstruct) {
		t.Fatalf("Analyze error = %v, want ErrUnsupportedLexicalConstruct", err)
	}
	if len(summary.Tables) != 0 || summary.CreateTables != 0 || summary.InsertTables != 0 {
		t.Fatalf("overlapping-comment analysis returned partial summary: %#v", summary)
	}
}

func TestAnalyzeClosesOrdinaryBlockCommentAcrossCarryStateSeam(t *testing.T) {
	// The closing star is offset 20 and slash is offset 21. Unlike "/*/", the
	// non-overlapping "/**/"-style close must remain valid across carried state.
	prefix := "/*" + strings.Repeat("x", 18)
	if len(prefix) != 20 {
		t.Fatalf("test prefix length = %d, want 20", len(prefix))
	}
	source := prefix + "*/\nCREATE TABLE retained (id int);\nINSERT INTO retained VALUES (1);\n-- " +
		strings.Repeat("padding", analyzerCarrySize/len("padding")+8)
	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 17})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != "retained" || len(summary.Tables[0].Regions) != 2 {
		t.Fatalf("ordinary block-comment seam analysis = %#v", summary.Tables)
	}
}

func TestAnalyzePreservesSafeWrappersAndNonCompoundTypeDDL(t *testing.T) {
	source := "# line-leading MySQL comment remains supported\n" +
		"CREATE TYPE mood AS ENUM ('sad', 'ok');\n" +
		"ALTER TYPE mood ADD VALUE 'great';\n" +
		"BEGIN;\n" +
		"INSERT INTO foo$bar$baz VALUES (1);\n" +
		"COMMIT;\n" +
		"BEGIN TRANSACTION;\n" +
		"CREATE TABLE retained (id int);\n" +
		"INSERT INTO retained VALUES (1);\n" +
		"COMMIT;\n"

	summary, err := Analyze(context.Background(), memReader{data: []byte(source)}, Options{ChunkSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	names := tableNames(summary)
	if len(names) != 2 {
		t.Fatalf("safe wrapper/type analysis tables = %#v", summary.Tables)
	}
	if table, ok := names["foo$bar$baz"]; !ok || len(table.Regions) != 1 || table.Regions[0].Kind != RegionInsert {
		t.Fatalf("dollar identifier table = %#v, present=%v", table, ok)
	}
	if table, ok := names["retained"]; !ok || len(table.Regions) != 2 {
		t.Fatalf("retained table = %#v, present=%v", table, ok)
	}
}
