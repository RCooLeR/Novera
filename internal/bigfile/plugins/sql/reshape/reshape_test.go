package reshape

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, in string, opts Options) (string, Summary) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "in.sql")
	dst := filepath.Join(dir, "out.sql")
	if err := os.WriteFile(src, []byte(in), 0o666); err != nil {
		t.Fatal(err)
	}
	sum, err := ReshapeInsertsFile(context.Background(), src, dst, opts)
	if err != nil {
		t.Fatalf("reshape: %v", err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), sum
}

func TestExplodeExtendedInsert(t *testing.T) {
	in := "INSERT INTO `t` (`a`,`b`) VALUES (1,'x'),(2,'y'),(3,'z');\n"
	out, sum := run(t, in, Options{Mode: ModeSingleRow})
	lines := nonEmptyLines(out)
	if len(lines) != 3 {
		t.Fatalf("want 3 single-row inserts, got %d:\n%s", len(lines), out)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, "INSERT INTO `t` (`a`,`b`) VALUES (") {
			t.Fatalf("line %d wrong prefix: %q", i, l)
		}
		if !strings.HasSuffix(l, ");") {
			t.Fatalf("line %d not terminated: %q", i, l)
		}
	}
	if sum.RowsSeen != 3 || sum.StatementsWritten != 3 || sum.InsertsRewritten != 1 {
		t.Fatalf("summary off: %+v", sum)
	}
}

func TestExplodePreservesUTF8QualifiedIdentifierBytes(t *testing.T) {
	prefix := "INSERT INTO база.пользователи (`значение`) VALUES "
	first := "(1,'café')"
	second := "(2,'λ')"
	out, summary := run(t, prefix+first+","+second+";\n", Options{Mode: ModeSingleRow})
	if strings.Count(out, prefix) != 2 || !strings.Contains(out, prefix+first+";") || !strings.Contains(out, prefix+second+";") {
		t.Fatalf("UTF-8 identifier or tuple bytes changed:\n%s", out)
	}
	if summary.RowsSeen != 2 || summary.InsertsRewritten != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestExplodeRespectsQuotedParens(t *testing.T) {
	// A value containing "),(" must not be split as a tuple boundary.
	in := "INSERT INTO `t` VALUES ('a),(b'),('c');\n"
	out, _ := run(t, in, Options{Mode: ModeSingleRow})
	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Fatalf("want 2 rows, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "'a),(b'") {
		t.Fatalf("quoted parens were split: %q", lines[0])
	}
}

func TestBatchSingleRowInserts(t *testing.T) {
	in := "INSERT INTO `t` VALUES (1);\nINSERT INTO `t` VALUES (2);\nINSERT INTO `t` VALUES (3);\n"
	out, sum := run(t, in, Options{Mode: ModeMultiRow, BatchSize: 2})
	lines := nonEmptyLines(out)
	// batch size 2 → (1),(2) then (3)
	if len(lines) != 2 {
		t.Fatalf("want 2 batched inserts, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "VALUES (1),(2)") {
		t.Fatalf("first batch wrong: %q", lines[0])
	}
	if !strings.Contains(lines[1], "VALUES (3)") {
		t.Fatalf("second batch wrong: %q", lines[1])
	}
	if sum.RowsSeen != 3 {
		t.Fatalf("rows seen = %d, want 3", sum.RowsSeen)
	}
}

func TestNonInsertCopiedVerbatim(t *testing.T) {
	in := "CREATE TABLE `t` (`a` int);\n/*!40000 SET x=1 */;\nINSERT INTO `t` VALUES (1),(2);\n"
	out, _ := run(t, in, Options{Mode: ModeSingleRow})
	if !strings.Contains(out, "CREATE TABLE `t` (`a` int);") {
		t.Fatalf("CREATE not preserved:\n%s", out)
	}
	if !strings.Contains(out, "/*!40000 SET x=1 */;") {
		t.Fatalf("conditional comment not preserved:\n%s", out)
	}
	if strings.Count(out, "INSERT INTO `t` VALUES (") != 2 {
		t.Fatalf("insert not exploded:\n%s", out)
	}
}

func TestExplodeAfterLeadingComment(t *testing.T) {
	// mysqldump prefixes each table's data with a comment block + blank line.
	in := "--\n-- Dumping data for table `users`\n--\n\nINSERT INTO `users` VALUES (1,'a'),(2,'b'),(3,'c');\n"
	out, sum := run(t, in, Options{Mode: ModeSingleRow})
	if sum.InsertsRewritten != 1 || sum.RowsSeen != 3 {
		t.Fatalf("comment-prefixed INSERT not reshaped: %+v", sum)
	}
	if strings.Count(out, "-- Dumping data for table `users`") != 1 {
		t.Fatalf("leading provenance comment was not preserved once:\n%s", out)
	}
	if strings.Count(out, "INSERT INTO `users` VALUES (") != 3 {
		t.Fatalf("want 3 single-row inserts:\n%s", out)
	}
}

func TestHashAndBlockCommentBeforeInsert(t *testing.T) {
	for _, in := range []string{
		"# a hash comment\nINSERT INTO `t` VALUES (1),(2);\n",
		"/* block\n comment */ INSERT INTO `t` VALUES (1),(2);\n",
	} {
		out, sum := run(t, in, Options{Mode: ModeSingleRow})
		if sum.InsertsRewritten != 1 || strings.Count(out, "INSERT INTO `t` VALUES (") != 2 {
			t.Fatalf("comment form not reshaped for %q ->\n%s", in, out)
		}
	}
}

func TestNoMergeAfterVerbatimStatement(t *testing.T) {
	// A verbatim statement ends in ';' with no newline; the following reshaped
	// INSERT must still start on its own line.
	in := "LOCK TABLES `t` WRITE;\nINSERT INTO `t` VALUES (1),(2);\n"
	out, _ := run(t, in, Options{Mode: ModeSingleRow})
	if strings.Contains(out, ";INSERT") {
		t.Fatalf("verbatim statement merged with reshaped INSERT:\n%s", out)
	}
	if strings.Count(out, "INSERT INTO `t` VALUES (") != 2 {
		t.Fatalf("insert not exploded:\n%s", out)
	}
}

func TestRejectsSamePath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.sql")
	if err := os.WriteFile(src, []byte("INSERT INTO t VALUES (1);"), 0o666); err != nil {
		t.Fatal(err)
	}
	_, err := ReshapeInsertsFile(context.Background(), src, src, Options{Mode: ModeSingleRow})
	if err == nil {
		t.Fatal("expected error for same path")
	}
}

func TestUnsupportedInsertFormsFailWithoutOutput(t *testing.T) {
	tests := []string{
		"INSERT INTO t VALUES (1),(2) ON DUPLICATE KEY UPDATE v=VALUES(v);",
		"INSERT INTO t VALUES (1),(2) RETURNING id;",
		"INSERT INTO t SELECT * FROM source;",
		"INSERT OVERWRITE t VALUES (1),(2);",
		"INSERT INTO t VALUES (1),(2)",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "in.sql")
			dst := filepath.Join(dir, "out.sql")
			if err := os.WriteFile(src, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReshapeInsertsFile(context.Background(), src, dst, Options{Mode: ModeSingleRow})
			if !errors.Is(err, ErrUnsupportedInsert) {
				t.Fatalf("error = %v, want ErrUnsupportedInsert", err)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed reshape published output: %v", statErr)
			}
			if source, readErr := os.ReadFile(src); readErr != nil || string(source) != input {
				t.Fatalf("source changed: %q, %v", source, readErr)
			}
		})
	}
}

func TestInvalidOptionsFailBeforeFilesystemWork(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.sql")
	output := filepath.Join(dir, "output.sql")
	tests := []Options{
		{Mode: Mode("future")},
		{Mode: ModeMultiRow, BatchSize: MaxBatchRows + 1},
	}
	for _, opts := range tests {
		if _, err := ReshapeInsertsFile(context.Background(), missing, output, opts); err == nil {
			t.Fatalf("options %+v unexpectedly succeeded", opts)
		}
		if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("invalid options created output: %v", statErr)
		}
	}
}

func TestAmbiguousDashPairFailsWithoutOutput(t *testing.T) {
	input := "SELECT 4--2;INSERT INTO t VALUES (1),(2);"
	dir := t.TempDir()
	src := filepath.Join(dir, "in.sql")
	dst := filepath.Join(dir, "out.sql")
	if err := os.WriteFile(src, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReshapeInsertsFile(context.Background(), src, dst, Options{Mode: ModeSingleRow})
	if !errors.Is(err, ErrUnsupportedLexicalConstruct) {
		t.Fatalf("error = %v, want ErrUnsupportedLexicalConstruct", err)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ambiguous reshape published output: %v", statErr)
	}
}

func TestCompoundAndClientOwnedSQLFailsWithoutOutput(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want error
	}{
		{"routine", "CREATE PROCEDURE p() BEGIN INSERT INTO audit VALUES (1),(2); END;", ErrUnsupportedCompoundStatement},
		{"dollar quote", "DO $$ BEGIN INSERT INTO audit VALUES (1),(2); END $$;", ErrUnsupportedCompoundStatement},
		{"psql copy", "\\copy imported FROM STDIN\nINSERT INTO payload VALUES (1),(2);", ErrUnsupportedCompoundStatement},
		{"sqlcmd batch", "SET NOCOUNT ON\nGO\nINSERT INTO payload VALUES (1),(2);", ErrUnsupportedCompoundStatement},
		{"oracle quote", "INSERT INTO retained VALUES (q'[x; INSERT INTO phantom VALUES (1),(2);]');", ErrUnsupportedLexicalConstruct},
		{"nested comment", "/* outer /* inner */ INSERT INTO phantom VALUES (1),(2); */", ErrUnsupportedLexicalConstruct},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "in.sql")
			dst := filepath.Join(dir, "out.sql")
			if err := os.WriteFile(src, []byte(test.sql), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReshapeInsertsFile(context.Background(), src, dst, Options{Mode: ModeSingleRow})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unsafe reshape published output: %v", statErr)
			}
		})
	}
}

func TestExecutableCommentInTargetInsertFailsClosed(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.sql")
	dst := filepath.Join(dir, "out.sql")
	input := "INSERT /*!40000 IGNORE */ INTO t VALUES (1),(2);"
	if err := os.WriteFile(src, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReshapeInsertsFile(context.Background(), src, dst, Options{Mode: ModeSingleRow})
	if !errors.Is(err, ErrUnsupportedInsert) {
		t.Fatalf("error = %v, want ErrUnsupportedInsert", err)
	}
	if _, statErr := os.Stat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed reshape published output: %v", statErr)
	}
}

func TestReshapePreservesWordPressSerializedTuplePayloadBytes(t *testing.T) {
	tupleA := `(1,'a:3:{s:4:"semi";s:4:"a;b;";s:5:"shape";s:3:"),(";s:5:"quote";s:8:"O\'Reilly";}')`
	tupleB := `(2,'O:8:"stdClass":2:{s:5:"value";s:5:"x),(;";s:5:"slash";s:3:"a\\b";}')`
	prefix := "INSERT INTO `wp_options` (`option_id`,`option_value`) VALUES "

	t.Run("explode", func(t *testing.T) {
		input := prefix + tupleA + "," + tupleB + ";\n"
		output, summary := run(t, input, Options{Mode: ModeSingleRow})
		want := prefix + tupleA + ";\n" + prefix + tupleB + ";\n\n"
		if output != want {
			t.Fatalf("serialized tuple bytes changed\n got: %q\nwant: %q", output, want)
		}
		if summary.RowsSeen != 2 || summary.InsertsRewritten != 1 {
			t.Fatalf("summary = %+v", summary)
		}
	})

	t.Run("batch", func(t *testing.T) {
		input := prefix + tupleA + ";\n" + prefix + tupleB + ";\n"
		output, summary := run(t, input, Options{Mode: ModeMultiRow, BatchSize: 10})
		wantPrefix := prefix + tupleA + "," + tupleB + ";\n"
		if !strings.HasPrefix(output, wantPrefix) || strings.TrimPrefix(output, wantPrefix) != "\n" {
			t.Fatalf("serialized tuple bytes changed\n got: %q\nwant prefix: %q", output, wantPrefix)
		}
		if summary.RowsSeen != 2 || summary.InsertsRewritten != 1 {
			t.Fatalf("summary = %+v", summary)
		}
	})
}

func TestClassifyStatementPrefixFailsClosedAtBoundedSeams(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  statementPrefixClass
	}{
		{name: "empty", input: "", want: statementPrefixUnknown},
		{name: "whitespace", input: " \r\n", want: statementPrefixUnknown},
		{name: "partial keyword", input: "/* complete */ ins", want: statementPrefixUnknown},
		{name: "unterminated block comment", input: "/* comment", want: statementPrefixUnknown},
		{name: "line comment without newline", input: "--comment", want: statementPrefixUnknown},
		{name: "partial dash opener", input: "-", want: statementPrefixUnknown},
		{name: "partial slash opener", input: "/", want: statementPrefixUnknown},
		{name: "partial byte order mark", input: string([]byte{0xef, 0xbb}), want: statementPrefixUnknown},
		{name: "comment then insert", input: "/* complete */ INSERT ", want: statementPrefixInsert},
		{name: "insert exact", input: "INSERT", want: statementPrefixInsert},
		{name: "identifier beginning insert", input: "inserted", want: statementPrefixOther},
		{name: "other keyword", input: "UPDATE ", want: statementPrefixOther},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyStatementPrefix([]byte(test.input)); got != test.want {
				t.Fatalf("classification = %d, want %d", got, test.want)
			}
		})
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
