package schemadiff

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"unsafe"
)

func TestParseColumnClonesBareNameFromNearLimitDDLBacking(t *testing.T) {
	ddl := []byte("tiny int" + strings.Repeat(" ", MaxStatementBytes-len("tiny int")-1))
	lexed, err := lexDDL(ddl)
	if err != nil {
		t.Fatal(err)
	}
	if len(lexed.tokens) != 2 {
		t.Fatalf("tokens = %#v, want bare name and type", lexed.tokens)
	}
	column, err := parseColumn(lexed.tokens)
	if err != nil {
		t.Fatal(err)
	}
	if column.Name != "tiny" {
		t.Fatalf("column name = %q, want tiny", column.Name)
	}
	if unsafe.StringData(column.Name) == unsafe.StringData(lexed.tokens[0].raw) {
		t.Fatal("retained column name still aliases the near-limit lexer input backing string")
	}
	if got := TableRetainedBytes(Table{Name: "t", Definition: ParsedTable{Items: []Column{column}}}); got < int64(len(column.Name)) {
		t.Fatalf("retained byte accounting = %d, smaller than cloned name", got)
	}
}

func TestParseTableCapturesColumnsConstraintsIndexesAndOptions(t *testing.T) {
	ddl := []byte("-- schema comment\n" +
		"CREATE TABLE `app`.`users` (\n" +
		"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
		"  `email` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,\n" +
		"  `manager_id` bigint DEFAULT NULL,\n" +
		"  PRIMARY KEY (`id`),\n" +
		"  UNIQUE KEY `uq_email` (`email`),\n" +
		"  CONSTRAINT `fk_manager` FOREIGN KEY (`manager_id`) REFERENCES `users` (`id`) ON DELETE SET NULL,\n" +
		"  CHECK (`id` > 0)\n" +
		") ENGINE=InnoDB DEFAULT CHARACTER SET=utf8mb4 COLLATE=utf8mb4_bin COMMENT='User table';")

	parsed := mustParseTable(t, ddl)
	if parsed.tableDisplay != "`app`.`users`" {
		t.Fatalf("table display = %q", parsed.tableDisplay)
	}
	if len(parsed.Items) != 3 {
		t.Fatalf("columns = %#v, want 3", parsed.Items)
	}
	if got := parsed.Items[1]; got.Name != "email" || got.Definition != "varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL" {
		t.Fatalf("email column = %#v", got)
	}
	if len(parsed.Constraints) != 3 {
		t.Fatalf("constraints = %#v, want primary/FK/check", parsed.Constraints)
	}
	if len(parsed.Indexes) != 1 {
		t.Fatalf("indexes = %#v, want unique index", parsed.Indexes)
	}
	if got := optionValues(parsed.Options); got["engine"] != "InnoDB" || got["charset"] != "utf8mb4" || got["collation"] != "utf8mb4_bin" || got["comment"] != "'User table'" {
		t.Fatalf("options = %#v", got)
	}
}

func TestParseColumnsCompatibilityAndEscapedIdentifiers(t *testing.T) {
	ddl := []byte("CREATE TABLE `odd``table` (`odd``column` decimal(10,2) NOT NULL, \"double\" text, `key` varchar(50), PRIMARY KEY (`odd``column`));")
	columns := ParseColumns(ddl)
	if len(columns) != 3 {
		t.Fatalf("columns = %#v, want 3", columns)
	}
	if columns[0].Name != "odd`column" || columns[0].Definition != "decimal(10,2) NOT NULL" {
		t.Fatalf("escaped backtick column = %#v", columns[0])
	}
	if columns[1].Name != "double" || columns[2].Name != "key" {
		t.Fatalf("quoted identifiers = %#v", columns)
	}
	parsed := mustParseTable(t, ddl)
	if parsed.tableDisplay != "`odd``table`" {
		t.Fatalf("table display = %q", parsed.tableDisplay)
	}
}

func TestParserPreservesQuotedLiteralCaseAndWhitespace(t *testing.T) {
	for _, tc := range []struct {
		name string
		old  string
		new  string
	}{
		{name: "default case", old: "varchar(50) DEFAULT 'ABC'", new: "varchar(50) DEFAULT 'abc'"},
		{name: "default whitespace", old: "varchar(50) DEFAULT 'a  b'", new: "varchar(50) DEFAULT 'a b'"},
		{name: "enum case", old: "enum('One','Two')", new: "enum('one','Two')"},
		{name: "escaped quote", old: "varchar(50) DEFAULT 'can''t'", new: "varchar(50) DEFAULT 'CAN''T'"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			oldTable := parsedTable("t", "CREATE TABLE t (v "+tc.old+");")
			newTable := parsedTable("t", "CREATE TABLE t (v "+tc.new+");")
			result := Diff([]Table{oldTable}, []Table{newTable})
			assertOneChangedTable(t, result)
			if len(result.ChangedTables[0].ChangedColumns) != 1 {
				t.Fatalf("literal change was not a column change: %#v", result)
			}
		})
	}
}

func TestParserNormalizesOnlyDocumentedCosmeticDifferences(t *testing.T) {
	oldDDL := `CREATE TABLE users (
 id INT NOT NULL DEFAULT 0,
 email VARCHAR(255) NOT NULL,
 PRIMARY KEY (id),
 UNIQUE KEY uq_email (email),
 CONSTRAINT fk_parent FOREIGN KEY(id) REFERENCES parent(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;`
	newDDL := `/* ordinary comments are cosmetic */
create table users(
 id int not null default 0,
 email varchar(255) not null,
 constraint fk_parent foreign key(id) references parent(id) on delete cascade,
 unique key uq_email(email), -- clause order is cosmetic
 primary key(id)
) collate = UTF8MB4_BIN engine = innodb character set = UTF8MB4;`
	result := Diff([]Table{parsedTable("users", oldDDL)}, []Table{parsedTable("users", newDDL)})
	if result.UnchangedCount != 1 || len(result.ChangedTables) != 0 {
		t.Fatalf("cosmetic formatting produced a diff: %#v", result)
	}
}

func TestParseTableRejectsTruncatedUnsupportedAndAmbiguousDDL(t *testing.T) {
	cases := map[string][]byte{
		"nil":                     nil,
		"empty":                   {},
		"missing terminator":      []byte("CREATE TABLE t (id int)"),
		"truncated body":          []byte("CREATE TABLE t (id int;"),
		"truncated string":        []byte("CREATE TABLE t (v text DEFAULT 'x);"),
		"truncated identifier":    []byte("CREATE TABLE `t (id int);"),
		"truncated comment":       []byte("CREATE TABLE t (id int) /* x"),
		"executable comment":      []byte("/*! CREATE TABLE t (id int); */"),
		"hint comment":            []byte("/*+ hint */ CREATE TABLE t (id int);"),
		"bracket identifier":      []byte("CREATE TABLE [t] (id int);"),
		"create as select":        []byte("CREATE TABLE t AS SELECT 1;"),
		"unsupported option":      []byte("CREATE TABLE t (id int) WITH SYSTEM VERSIONING;"),
		"unknown body clause":     []byte("CREATE TABLE t (id int, LIKE other);"),
		"unknown column type":     []byte("CREATE TABLE t (id proprietary_type);"),
		"unknown column clause":   []byte("CREATE TABLE t (id int MASKED WITH (FUNCTION='x'));"),
		"unknown constraint tail": []byte("CREATE TABLE t (id int, PRIMARY KEY(id) MYSTERY);"),
		"multiple statements":     []byte("CREATE TABLE t (id int); CREATE TABLE u (id int);"),
		"backslash SQL mode":      []byte("CREATE TABLE t (v text DEFAULT 'a\\'b');"),
		"trailing option comma":   []byte("CREATE TABLE t (id int) ENGINE=InnoDB,;"),
		"malformed number":        []byte("CREATE TABLE t (v numeric DEFAULT 1.2.3);"),
		"malformed dollar quote":  []byte("CREATE TABLE t (v text DEFAULT $1$);"),
	}
	for name, ddl := range cases {
		name, ddl := name, ddl
		t.Run(name, func(t *testing.T) {
			parsed := ParseTable(ddl)
			if parsed.Status != ParseUnknown || parsed.Reason == "" {
				t.Fatalf("parse = %#v, want explicit unknown", parsed)
			}
			if parsed.Items == nil || parsed.Constraints == nil || parsed.Indexes == nil || parsed.Options == nil {
				t.Fatalf("unknown parse returned nil collections: %#v", parsed)
			}
		})
	}
}

func TestParseTableSupportsConservativeMySQLPostgreSQLAndSQLiteSubset(t *testing.T) {
	cases := map[string]string{
		"mysql": `CREATE TABLE t (
 id bigint unsigned NOT NULL AUTO_INCREMENT,
 updated_at timestamp(6) DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
 name varchar(40) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin COMMENT 'name',
 slug varchar(40) GENERATED ALWAYS AS (lower(name)) STORED,
 PRIMARY KEY(id), KEY ix_name(name) USING BTREE
) ENGINE=InnoDB;`,
		"postgresql": `CREATE TABLE app.t (
 id bigint GENERATED BY DEFAULT AS IDENTITY,
 ts timestamp(6) with time zone DEFAULT CURRENT_TIMESTAMP,
	amount numeric(10,2) DEFAULT (-1),
	scientific numeric DEFAULT 1e-3,
	note text DEFAULT $tag$A B$tag$,
	name character varying(20) COLLATE "C",
 parent uuid REFERENCES app.parent(id) ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED,
 calc int GENERATED ALWAYS AS ((id + 1)) STORED
) TABLESPACE main_ts;`,
		"sqlite": `CREATE TABLE t (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 value TEXT NOT NULL ON CONFLICT FAIL DEFAULT ('x'),
 generated TEXT AS (lower(value)) VIRTUAL,
 parent_id INT REFERENCES parent(id) ON UPDATE CASCADE
) WITHOUT ROWID, STRICT;`,
	}
	for name, ddl := range cases {
		name, ddl := name, ddl
		t.Run(name, func(t *testing.T) {
			mustParseTable(t, []byte(ddl))
		})
	}
}

func TestParseTableEnforcesStatementColumnDefinitionAndNestingLimits(t *testing.T) {
	base := []byte("CREATE TABLE t (id int);")
	paddingLength := MaxStatementBytes - len(base) - len("/**/")
	exact := append([]byte("/*"), bytes.Repeat([]byte{'x'}, paddingLength)...)
	exact = append(exact, []byte("*/")...)
	exact = append(exact, base...)
	if len(exact) != MaxStatementBytes {
		t.Fatalf("exact fixture length = %d", len(exact))
	}
	if parsed := ParseTable(exact); parsed.Status != ParseComplete {
		t.Fatalf("exact limit rejected: %s", parsed.Reason)
	}
	if parsed := ParseTable(append(exact, ' ')); parsed.Status != ParseUnknown || !strings.Contains(parsed.Reason, "exceeds") {
		t.Fatalf("limit+1 parse = %#v", parsed)
	}

	var columns strings.Builder
	columns.WriteString("CREATE TABLE t (")
	for i := 0; i <= MaxColumnCount; i++ {
		if i > 0 {
			columns.WriteByte(',')
		}
		columns.WriteString("c")
		columns.WriteString(intString(i))
		columns.WriteString(" int")
	}
	columns.WriteString(");")
	if parsed := ParseTable([]byte(columns.String())); parsed.Status != ParseUnknown || !strings.Contains(parsed.Reason, "column count") {
		t.Fatalf("column limit parse = %#v", parsed)
	}

	var entries strings.Builder
	entries.WriteString("CREATE TABLE t (id int")
	for i := 0; i < MaxSchemaEntryCount; i++ {
		entries.WriteString(",CHECK(id >= 0)")
	}
	entries.WriteString(");")
	if parsed := ParseTable([]byte(entries.String())); parsed.Status != ParseUnknown || !strings.Contains(parsed.Reason, "schema entry count") {
		t.Fatalf("entry limit parse = %#v", parsed)
	}

	oversizedLiteral := "CREATE TABLE t (v text DEFAULT '" + strings.Repeat("x", MaxDefinitionBytes+1) + "');"
	if parsed := ParseTable([]byte(oversizedLiteral)); parsed.Status != ParseUnknown || !strings.Contains(parsed.Reason, "definition limit") {
		t.Fatalf("definition limit parse = %#v", parsed)
	}

	deep := "CREATE TABLE t (v int DEFAULT " + strings.Repeat("(", MaxNestingDepth+1) + "1" + strings.Repeat(")", MaxNestingDepth+1) + ");"
	if parsed := ParseTable([]byte(deep)); parsed.Status != ParseUnknown || !strings.Contains(parsed.Reason, "nesting") {
		t.Fatalf("nesting limit parse = %#v", parsed)
	}
}

func TestParseTableReaderIsChunkIndependentAndBounded(t *testing.T) {
	ddl := []byte("/* seam comment */ CREATE TABLE `t``x` (`v` enum('A','b,c') DEFAULT 'A  B', CHECK (`v` <> '')) ENGINE=InnoDB;")
	direct := mustParseTable(t, ddl)
	for _, chunkSize := range []int{1, 2, 3, 7, 31, 64} {
		reader := &fixedChunkReader{data: ddl, chunkSize: chunkSize}
		chunked := ParseTableReader(reader)
		if chunked.Status != ParseComplete {
			t.Fatalf("chunk %d: %s", chunkSize, chunked.Reason)
		}
		result := Diff(
			[]Table{{Name: "t`x", Definition: direct}},
			[]Table{{Name: "t`x", Definition: chunked}},
		)
		if result.UnchangedCount != 1 || len(result.ChangedTables) != 0 {
			t.Fatalf("chunk %d changed semantics: %#v", chunkSize, result)
		}
	}

	failing := ParseTableReader(io.MultiReader(strings.NewReader("CREATE TABLE t (id int)"), errorReader{}))
	if failing.Status != ParseUnknown || !strings.Contains(failing.Reason, "read CREATE TABLE") {
		t.Fatalf("reader failure = %#v", failing)
	}

	largeReader := &countingRepeatingReader{remaining: MaxStatementBytes + 100}
	parsed := ParseTableReader(largeReader)
	if parsed.Status != ParseUnknown || largeReader.read > MaxStatementBytes+1 {
		t.Fatalf("bounded reader parse = %#v, read=%d", parsed, largeReader.read)
	}
}

func TestParseAndDiffContextCancellationIsNotReportedAsUnknown(t *testing.T) {
	ctx := &cancelAfterChecksContext{Context: context.Background(), remaining: 3, done: make(chan struct{})}
	ddl := []byte("/*" + strings.Repeat("x", 64<<10) + "*/ CREATE TABLE t (id int);")
	parsed, err := ParseTableContext(ctx, ddl)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ParseTableContext error = %v, want context.Canceled", err)
	}
	if parsed.Status != "" {
		t.Fatalf("canceled parse returned a publishable status: %#v", parsed)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := DiffContext(canceled, []Table{parsedTable("t", "CREATE TABLE t (id int);")}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DiffContext error = %v, want context.Canceled", err)
	}
	if result.UnchangedCount != 0 || len(result.AddedTables) != 0 || len(result.RemovedTables) != 0 || len(result.ChangedTables) != 0 {
		t.Fatalf("canceled diff returned a partial result: %#v", result)
	}
}

func mustParseTable(t *testing.T, ddl []byte) ParsedTable {
	t.Helper()
	parsed := ParseTable(ddl)
	if parsed.Status != ParseComplete {
		t.Fatalf("parse unknown: %s\nDDL: %s", parsed.Reason, ddl)
	}
	return parsed
}

func parsedTable(name, ddl string) Table {
	return Table{Name: name, Definition: ParseTable([]byte(ddl))}
}

func optionValues(options []TableOption) map[string]string {
	values := make(map[string]string, len(options))
	for _, option := range options {
		values[option.Name] = option.Value
	}
	return values
}

func intString(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = digits[value%10]
		value /= 10
	}
	return string(buf[pos:])
}

type fixedChunkReader struct {
	data      []byte
	chunkSize int
	offset    int
}

type cancelAfterChecksContext struct {
	context.Context
	mu        sync.Mutex
	remaining int
	done      chan struct{}
	once      sync.Once
}

func (c *cancelAfterChecksContext) Done() <-chan struct{} { return c.done }

func (c *cancelAfterChecksContext) Err() error {
	c.mu.Lock()
	if c.remaining > 0 {
		c.remaining--
	}
	canceled := c.remaining == 0
	c.mu.Unlock()
	if canceled {
		c.once.Do(func() { close(c.done) })
		return context.Canceled
	}
	return nil
}

func (r *fixedChunkReader) Read(buf []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := r.chunkSize
	if n > len(buf) {
		n = len(buf)
	}
	if remaining := len(r.data) - r.offset; n > remaining {
		n = remaining
	}
	copy(buf, r.data[r.offset:r.offset+n])
	r.offset += n
	return n, nil
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("injected reader failure")
}

type countingRepeatingReader struct {
	remaining int
	read      int
}

func (r *countingRepeatingReader) Read(buf []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(buf)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		buf[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}
