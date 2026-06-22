package reshape

import (
	"context"
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
	lines := nonEmptyLines(out)
	if len(lines) != 3 {
		t.Fatalf("want 3 single-row inserts, got %d:\n%s", len(lines), out)
	}
	if sum.InsertsRewritten != 1 || sum.RowsSeen != 3 {
		t.Fatalf("comment-prefixed INSERT not reshaped: %+v", sum)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "INSERT INTO `users` VALUES (") {
			t.Fatalf("line not reshaped: %q", l)
		}
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

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
