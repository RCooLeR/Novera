package analyze

import (
	"context"
	"io"
	"strings"
	"testing"
)

type memReader struct {
	data []byte
}

func (m memReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m memReader) Size() int64 {
	return int64(len(m.data))
}

func TestAnalyzeFindsTablesAndSettings(t *testing.T) {
	text := strings.Join([]string{
		"-- MySQL dump 10.13  Distrib 8.0",
		"CREATE DEFINER=`root`@`localhost` TABLE `users` (",
		"  id int NOT NULL",
		") ENGINE=MyISAM DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;",
		"INSERT INTO `users` VALUES (1);",
		"CREATE TABLE orders (id int) DEFAULT CHARSET=utf8mb4;",
		"INSERT INTO orders VALUES (2);",
	}, "\n")

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.MysqldumpHeader {
		t.Fatal("expected mysqldump header")
	}
	if summary.DefinerCount != 1 {
		t.Fatalf("definer count = %d, want 1", summary.DefinerCount)
	}
	if summary.Charsets["utf8mb4"] != 2 {
		t.Fatalf("charset count = %d, want 2", summary.Charsets["utf8mb4"])
	}
	if summary.Collations["utf8mb4_unicode_ci"] != 1 {
		t.Fatalf("collation count = %d, want 1", summary.Collations["utf8mb4_unicode_ci"])
	}
	if len(summary.Tables) != 2 {
		t.Fatalf("tables = %d, want 2", len(summary.Tables))
	}
	if summary.Tables[0].Name != "users" {
		t.Fatalf("first table = %q", summary.Tables[0].Name)
	}
	if summary.Tables[0].CreateOffset < 0 || summary.Tables[0].InsertOffset < 0 {
		t.Fatalf("users offsets = %+v", summary.Tables[0])
	}
}

func TestAnalyzeHandlesChunkBoundaries(t *testing.T) {
	text := "aaa CREATE TABLE `alpha` (id int);\nzzz INSERT INTO `alpha` VALUES (1);"
	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 {
		t.Fatalf("tables = %d, want 1", len(summary.Tables))
	}
	if summary.Tables[0].Name != "alpha" {
		t.Fatalf("table = %q", summary.Tables[0].Name)
	}
	if summary.Tables[0].CreateOffset < 0 || summary.Tables[0].InsertOffset < 0 {
		t.Fatalf("alpha offsets = %+v", summary.Tables[0])
	}
}

func TestAnalyzeIgnoresStatementsInsideStringsAndComments(t *testing.T) {
	text := strings.Join([]string{
		"-- CREATE TABLE ignored_comment (id int);",
		"INSERT INTO logs VALUES ('CREATE TABLE ignored_string (id int)', 'INSERT INTO ignored_insert VALUES (1)');",
		"/* INSERT INTO ignored_block VALUES (1); */",
		"CREATE TABLE `real_table` (id int) DEFAULT CHARSET=utf8mb4;",
		"INSERT INTO `real_table` VALUES (1);",
	}, "\n")

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 23})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 2 {
		t.Fatalf("tables = %#v, want logs insert-only plus real_table", summary.Tables)
	}
	if summary.Tables[0].Name != "logs" || summary.Tables[0].CreateOffset >= 0 || summary.Tables[0].InsertOffset < 0 {
		t.Fatalf("first table = %#v, want insert-only logs", summary.Tables[0])
	}
	if summary.Tables[1].Name != "real_table" || summary.Tables[1].CreateOffset < 0 || summary.Tables[1].InsertOffset < 0 {
		t.Fatalf("second table = %#v, want real_table", summary.Tables[1])
	}
	for _, table := range summary.Tables {
		if strings.Contains(table.Name, "ignored") {
			t.Fatalf("false-positive table from string/comment: %#v", summary.Tables)
		}
	}
}

func TestAnalyzePreservesCaseSensitiveTableNames(t *testing.T) {
	text := strings.Join([]string{
		"CREATE TABLE `Users` (id int);",
		"INSERT INTO `Users` VALUES (1);",
		"CREATE TABLE `users` (id int);",
		"INSERT INTO `users` VALUES (2);",
	}, "\n")

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 17})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 2 {
		t.Fatalf("tables = %#v, want Users and users as separate entries", summary.Tables)
	}
	if got, want := summary.Tables[0].Name, "Users"; got != want {
		t.Fatalf("table 0 = %q, want %q", got, want)
	}
	if got, want := summary.Tables[1].Name, "users"; got != want {
		t.Fatalf("table 1 = %q, want %q", got, want)
	}
	for _, table := range summary.Tables {
		if table.CreateOffset < 0 || table.InsertOffset < 0 {
			t.Fatalf("table offsets = %+v, want create and insert offsets", table)
		}
	}
}

func TestAnalyzeKeepsStatsNamesNormalizedLowercase(t *testing.T) {
	text := "CREATE TABLE `Users` (id int) DEFAULT CHARSET=UTF8MB4 COLLATE=UTF8MB4_UNICODE_CI;"

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 11})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Charsets["utf8mb4"] != 1 {
		t.Fatalf("charsets = %#v, want lowercase utf8mb4 count", summary.Charsets)
	}
	if summary.Collations["utf8mb4_unicode_ci"] != 1 {
		t.Fatalf("collations = %#v, want lowercase utf8mb4_unicode_ci count", summary.Collations)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != "Users" {
		t.Fatalf("tables = %#v, want table name case preserved", summary.Tables)
	}
}

func TestAnalyzeFindsCharsetAndCollationVariants(t *testing.T) {
	text := strings.Join([]string{
		"CREATE TABLE `table_opts` (id int) DEFAULT CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;",
		"CREATE TABLE `column_opts` (name varchar(20) CHARACTER SET latin1 COLLATE latin1_swedish_ci);",
		"CREATE TABLE `quoted_opts` (id int) CHARSET=`cp1251` COLLATE=`cp1251_general_ci`;",
	}, "\n")

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 23})
	if err != nil {
		t.Fatal(err)
	}
	wantCharsets := map[string]int{
		"utf8mb4": 1,
		"latin1":  1,
		"cp1251":  1,
	}
	for charset, want := range wantCharsets {
		if got := summary.Charsets[charset]; got != want {
			t.Fatalf("charset %q count = %d, want %d; all charsets=%#v", charset, got, want, summary.Charsets)
		}
	}
	wantCollations := map[string]int{
		"utf8mb4_0900_ai_ci": 1,
		"latin1_swedish_ci":  1,
		"cp1251_general_ci":  1,
	}
	for collation, want := range wantCollations {
		if got := summary.Collations[collation]; got != want {
			t.Fatalf("collation %q count = %d, want %d; all collations=%#v", collation, got, want, summary.Collations)
		}
	}
}

func TestAnalyzeFindsLongDefinerCreateAcrossChunkBoundary(t *testing.T) {
	longUser := strings.Repeat("u", 1500)
	longHost := strings.Repeat("h", 1500)
	statement := "CREATE DEFINER=`" + longUser + "`@`" + longHost + "` TABLE `BoundaryCase` (id int);"
	text := strings.Repeat("x", 700) + statement + "\n" + strings.Repeat("y", 20*1024)

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	if summary.DefinerCount != 1 {
		t.Fatalf("definer count = %d, want 1", summary.DefinerCount)
	}
	if len(summary.Tables) != 1 {
		t.Fatalf("tables = %#v, want BoundaryCase", summary.Tables)
	}
	if got, want := summary.Tables[0].Name, "BoundaryCase"; got != want {
		t.Fatalf("table = %q, want %q", got, want)
	}
	if summary.Tables[0].CreateOffset != int64(700) {
		t.Fatalf("create offset = %d, want 700", summary.Tables[0].CreateOffset)
	}
}

func tableNames(summary Summary) map[string]Table {
	out := make(map[string]Table, len(summary.Tables))
	for _, t := range summary.Tables {
		out[t.Name] = t
	}
	return out
}

// A string literal larger than the chunk used to leave the next window starting
// mid-literal with the opening quote out of view, so blob text that looks like
// CREATE TABLE / INSERT INTO was scanned as live SQL (phantom tables). The lexer
// state must now carry across windows. Fillers exceed 16 KiB so real streaming
// (multiple processWindow calls) occurs.
func TestAnalyzeIgnoresStatementsInsideLiteralSpanningChunks(t *testing.T) {
	filler := strings.Repeat("x", 20000)
	literal := filler + " CREATE TABLE phantom (id int); INSERT INTO ghost VALUES (1); " + filler
	text := "INSERT INTO realtbl VALUES ('" + literal + "');\n" +
		"CREATE TABLE after_it (id int);\n" +
		"INSERT INTO after_it VALUES (1);\n"

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	names := tableNames(summary)
	if _, ok := names["phantom"]; ok {
		t.Fatalf("phantom table leaked from inside a boundary-spanning literal: %#v", summary.Tables)
	}
	if _, ok := names["ghost"]; ok {
		t.Fatalf("ghost table leaked from inside a boundary-spanning literal: %#v", summary.Tables)
	}
	if _, ok := names["realtbl"]; !ok {
		t.Fatalf("realtbl missing: %#v", summary.Tables)
	}
	if at, ok := names["after_it"]; !ok || at.CreateOffset < 0 || at.InsertOffset < 0 {
		t.Fatalf("after_it should be discovered with create+insert: %#v", summary.Tables)
	}
}

// Guards the polarity-flip / silent-loss case: an apostrophe inside a
// boundary-spanning literal must not flip masking and drop a genuine table that
// comes after the literal.
func TestAnalyzeFindsGenuineTableAfterLiteralWithApostrophe(t *testing.T) {
	filler := strings.Repeat("y", 20000)
	// it\'s — a backslash-escaped apostrophe deep inside a multi-window literal.
	literal := filler + " it\\'s a value with CREATE TABLE bait inside " + filler
	text := "INSERT INTO data_tbl VALUES ('" + literal + "');\n" +
		"CREATE TABLE genuine_after (id int);\n"

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	names := tableNames(summary)
	if _, ok := names["bait"]; ok {
		t.Fatalf("bait leaked from inside the literal: %#v", summary.Tables)
	}
	if g, ok := names["genuine_after"]; !ok || g.CreateOffset < 0 {
		t.Fatalf("genuine_after must be discovered after the literal: %#v", summary.Tables)
	}
}

// A block comment larger than the chunk must stay masked across windows.
func TestAnalyzeIgnoresStatementsInsideBlockCommentSpanningChunks(t *testing.T) {
	filler := strings.Repeat("z", 20000)
	text := "/* " + filler + " CREATE TABLE phantom_block (id int); " + filler + " */\n" +
		"CREATE TABLE real_after_block (id int);\n"

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	names := tableNames(summary)
	if _, ok := names["phantom_block"]; ok {
		t.Fatalf("phantom_block leaked from inside a boundary-spanning block comment: %#v", summary.Tables)
	}
	if _, ok := names["real_after_block"]; !ok {
		t.Fatalf("real_after_block must be discovered after the comment: %#v", summary.Tables)
	}
}

// db-qualified names (`db`.`tbl`, db.tbl) must record the TABLE, not the
// database, and quoted names with spaces must be captured whole — otherwise
// multi-database dumps collapse under the db name and extract fails.
func TestAnalyzeCapturesTableSegmentOfQualifiedNames(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "backtick qualified",
			text: "CREATE TABLE `mydb`.`users` (id int);\nINSERT INTO `mydb`.`orders` VALUES (1);\n",
			want: []string{"orders", "users"},
		},
		{
			name: "bare qualified",
			text: "CREATE TABLE mydb.users (id int);\nINSERT INTO mydb.orders VALUES (1);\n",
			want: []string{"orders", "users"},
		},
		{
			name: "spaced quoted name",
			text: "CREATE TABLE `weird name` (id int);\nINSERT INTO `weird name` VALUES (1);\n",
			want: []string{"weird name"},
		},
		{
			name: "qualified spaced table",
			text: "INSERT INTO `my-db`.`tbl with space` VALUES (1);\n",
			want: []string{"tbl with space"},
		},
		{
			name: "unqualified still works",
			text: "CREATE TABLE `users` (id int);\nINSERT INTO users VALUES (1);\n",
			want: []string{"users"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary, err := Analyze(context.Background(), memReader{data: []byte(tc.text)}, Options{ChunkSize: 64})
			if err != nil {
				t.Fatal(err)
			}
			names := tableNames(summary)
			for _, w := range tc.want {
				if _, ok := names[w]; !ok {
					t.Fatalf("want table %q, got %#v", w, summary.Tables)
				}
			}
			if len(names) != len(tc.want) {
				t.Fatalf("table count = %d (%#v), want %d", len(names), summary.Tables, len(tc.want))
			}
			for _, bad := range []string{"mydb", "my-db", "weird", "tbl"} {
				if _, ok := names[bad]; ok {
					t.Fatalf("captured %q (a database/partial name) instead of the table: %#v", bad, summary.Tables)
				}
			}
		})
	}
}
