package csv

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type sqlCountingReader struct {
	source io.Reader
	reads  int
}

func (r *sqlCountingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.source.Read(p)
}

type sqlCountingWriter struct{ writes int }

func (w *sqlCountingWriter) Write(p []byte) (int, error) {
	w.writes++
	return len(p), nil
}

func TestSQLParityRejectsInvalidConfigurationBeforeIO(t *testing.T) {
	tests := []SQLConvertOptions{
		{TableName: "records", Columns: []string{"id"}, InsertMode: SQLInsertMode("INSERT; DROP TABLE records")},
		{TableName: "records", Columns: []string{"id", "copy"}, SourceColumns: []int{0, 0}},
		{TableName: "records", Columns: []string{"id"}, MaxBatchBytes: -1},
		{TableName: "records", Columns: []string{"id"}, MaxBatchBytes: MaxSQLInsertBatchBytes + 1},
		{TableName: "records", Columns: []string{"id"}, InsertBatchSize: -1},
		{TableName: "records", Columns: []string{"id"}, InsertBatchSize: MaxSQLInsertBatchSize + 1},
		{TableName: "records", Columns: []string{"id"}, MaxFieldBytes: -1},
		{TableName: "records", Columns: []string{"id"}, OnInvalidValue: InvalidValuePolicy("coerce")},
		{TableName: "records", Columns: []string{"id"}, ColumnTypes: []string{"BOOLISH"}},
		{Delimiter: '\n', TableName: "records", Columns: []string{"id"}},
	}
	for _, opts := range tests {
		source := &sqlCountingReader{source: strings.NewReader("1\n")}
		output := &sqlCountingWriter{}
		if _, err := ConvertToSQL(context.Background(), source, output, opts); err == nil {
			t.Fatalf("invalid options were accepted: %+v", opts)
		}
		if source.reads != 0 || output.writes != 0 {
			t.Fatalf("invalid options touched I/O: reads=%d writes=%d", source.reads, output.writes)
		}
	}
}

func TestSQLParityCanonicalizesAndValidatesMySQLBoolean(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("TRUE\nfalse\n1\n0\nNULL\n"), &out, SQLConvertOptions{
		TableName: "records", Columns: []string{"active"}, ColumnTypes: []string{SQLTypeBoolean},
		NullValues: []string{"NULL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(1)", "(0)", "NULL"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("SQL = %q, want %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "'TRUE'") || strings.Contains(out.String(), "'false'") {
		t.Fatalf("BOOLEAN values were quoted instead of canonicalized: %q", out.String())
	}

	for _, invalid := range []string{"yes\n", " true\n", "2\n", "\"\"\n"} {
		out.Reset()
		_, err := ConvertToSQL(context.Background(), strings.NewReader(invalid), &out, SQLConvertOptions{
			TableName: "records", Columns: []string{"active"}, ColumnTypes: []string{SQLTypeBoolean},
		})
		if err == nil || !strings.Contains(err.Error(), "MySQL BOOLEAN") {
			t.Fatalf("value %q error = %v, want BOOLEAN validation", invalid, err)
		}
	}
}

func TestSQLParityBoundsAggregateInsertBytes(t *testing.T) {
	header, err := sqlInsertBatchHeader(SQLInsertModeInsert, "records", []string{"value"})
	if err != nil {
		t.Fatal(err)
	}
	tuple, _, _, err := sqlValuesTuple([]string{"x"}, nil, []string{SQLTypeText}, InvalidValueFail)
	if err != nil {
		t.Fatal(err)
	}
	framing := int64(len(header) + len(sqlInsertTerminator))
	limit := framing + 2*int64(len(tuple)) + int64(len(sqlInsertTupleSeparator))
	var out strings.Builder
	summary, err := ConvertToSQL(context.Background(), strings.NewReader("x\nx\nx\n"), &out, SQLConvertOptions{
		TableName: "records", Columns: []string{"value"},
		InsertBatchSize: MaxSQLInsertBatchSize, MaxBatchBytes: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowsWritten != 3 || strings.Count(out.String(), "INSERT INTO") != 2 {
		t.Fatalf("summary = %+v, SQL = %q; want three rows in two bounded statements", summary, out.String())
	}
}

type sqlChunkWriter struct {
	builder strings.Builder
	max     int
}

func (w *sqlChunkWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.builder.Write(p)
}

type sqlZeroWriter struct{}

func (sqlZeroWriter) Write([]byte) (int, error) { return 0, nil }

func TestSQLParityHandlesShortWritersExactly(t *testing.T) {
	header, err := sqlInsertBatchHeader(SQLInsertModeInsertIgnore, "records", []string{"id"})
	if err != nil {
		t.Fatal(err)
	}
	tuples := []string{"('1')", "('2')"}
	writer := &sqlChunkWriter{max: 3}
	if err := writeSQLInsertBatch(writer, header, tuples); err != nil {
		t.Fatal(err)
	}
	want := header + strings.Join(tuples, sqlInsertTupleSeparator) + sqlInsertTerminator
	if writer.builder.String() != want {
		t.Fatalf("short-writer output = %q, want %q", writer.builder.String(), want)
	}
	if err := writeSQLInsertBatch(sqlZeroWriter{}, header, tuples); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress error = %v, want io.ErrShortWrite", err)
	}
}

func TestSQLParityIdentifierLimitsAndCaseFoldedDeduplication(t *testing.T) {
	maximum := strings.Repeat("😀", MaxSQLIdentifierRunes)
	if len(maximum) != MaxSQLIdentifierBytes {
		t.Fatalf("maximum identifier is %d bytes, want %d", len(maximum), MaxSQLIdentifierBytes)
	}
	if err := ValidateSQLConvertOptions(SQLConvertOptions{TableName: maximum}); err != nil {
		t.Fatalf("maximum identifier rejected: %v", err)
	}
	if err := ValidateSQLConvertOptions(SQLConvertOptions{TableName: maximum + "x"}); err == nil {
		t.Fatal("oversized identifier accepted")
	}

	names := normalizeSQLColumnNames([]string{"a", "a", "a_2", "A"})
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if utf8.RuneCountInString(name) > MaxSQLIdentifierRunes {
			t.Fatalf("normalized identifier exceeds character limit: %q", name)
		}
		key := foldSQLIdentifier(name)
		if _, exists := seen[key]; exists {
			t.Fatalf("normalized identifiers are not unique: %#v", names)
		}
		seen[key] = struct{}{}
	}
}

func TestSQLParityUsesStrictCSVParsingAndMappingRanges(t *testing.T) {
	var out strings.Builder
	if _, err := ConvertToSQL(context.Background(), strings.NewReader("id,name\n1,bad\"quote\n"), &out, SQLConvertOptions{
		TableName: "records", HasHeader: true,
	}); err == nil {
		t.Fatal("malformed CSV quote was accepted")
	}
	out.Reset()
	if _, err := ConvertToSQL(context.Background(), strings.NewReader("id\n1\n"), &out, SQLConvertOptions{
		TableName: "records", HasHeader: true,
		Columns: []string{"id", "missing"}, SourceColumns: []int{0, 1},
	}); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("mapping error = %v, want out-of-range rejection", err)
	}
	if out.Len() != 0 {
		t.Fatalf("invalid mapping wrote output: %q", out.String())
	}
}

func TestSQLParityInferredBooleanUsesCanonicalValues(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	output := filepath.Join(dir, "output.sql")
	// Inference remains conservative: bare 1/0 are also valid integer data and
	// are only treated as BOOLEAN when the caller explicitly selects that type.
	if err := os.WriteFile(input, []byte("active\ntrue\nFALSE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ConvertToSQLFile(context.Background(), input, output, SQLConvertOptions{
		Delimiter: ',', TableName: "flags", HasHeader: true, IncludeCreateTable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	if !strings.Contains(sql, "`active` BOOLEAN") || strings.Contains(sql, "'true'") || strings.Contains(sql, "'FALSE'") {
		t.Fatalf("inferred BOOLEAN SQL was not canonical: %q", sql)
	}
}

func TestSQLParityLateInvalidInferredBooleanDoesNotPublish(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	output := filepath.Join(dir, "output.sql")
	var data strings.Builder
	data.WriteString("active\n")
	for i := 0; i < DefaultSchemaMaxRows+25; i++ {
		data.WriteString("true\n")
	}
	data.WriteString("yes\n")
	if err := os.WriteFile(input, []byte(data.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ConvertToSQLFile(context.Background(), input, output, SQLConvertOptions{
		Delimiter: ',', TableName: "flags", HasHeader: true, IncludeCreateTable: true,
	})
	if err == nil || !strings.Contains(err.Error(), "MySQL BOOLEAN") {
		t.Fatalf("error = %v, want late BOOLEAN validation", err)
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("late invalid BOOLEAN published output: %v", statErr)
	}
}

func TestSQLParityUnknownTypeRejectsBeforeInputOrOutput(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "output.sql")
	_, err := ConvertToSQLFile(context.Background(), filepath.Join(dir, "missing.csv"), output, SQLConvertOptions{
		Delimiter: ',', TableName: "flags", Columns: []string{"active"}, ColumnTypes: []string{"BOOLISH"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid SQL column type") {
		t.Fatalf("error = %v, want invalid type", err)
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid type created output: %v", statErr)
	}
}
