package csv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConvertToSQLUsesHeaderAndEscapesValues(t *testing.T) {
	var out strings.Builder
	summary, err := ConvertToSQL(context.Background(), strings.NewReader("id,name,note\n1,Ada,hello\n2,O'Neil,NULL\n3,Grace,C:\\Users\\Public\n"), &out, SQLConvertOptions{
		TableName:       "people",
		HasHeader:       true,
		NullValues:      []string{"NULL"},
		InsertBatchSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "INSERT INTO `people` (`id`, `name`, `note`) VALUES\n  ('1', 'Ada', 'hello'),\n  ('2', 'O''Neil', NULL);\nINSERT INTO `people` (`id`, `name`, `note`) VALUES\n  ('3', 'Grace', 'C:\\\\Users\\\\Public');\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if summary.RecordsRead != 4 || summary.RowsWritten != 3 || summary.Columns != 3 {
		t.Fatalf("summary = %+v, want 4 records, 3 rows, 3 columns", summary)
	}
	if summary.Dialect != SQLDialectMySQL {
		t.Fatalf("dialect = %q, want mysql", summary.Dialect)
	}
}

func TestQuoteSQLStringEscapesMySQLSpecials(t *testing.T) {
	value := "line\nnext\rtab\tback\bslash\\quote'nul" + string(rune(0)) + "ctrlz" + string(rune(0x1A))
	got, err := quoteSQLString(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `'line\nnext\rtab\tback\bslash\\quote''nul\0ctrlz\Z'`
	if got != want {
		t.Fatalf("quoteSQLString() = %q, want %q", got, want)
	}
}

func TestQuoteSQLStringRejectsUnsafeControlCharacters(t *testing.T) {
	_, err := quoteSQLString("unsafe" + string(rune(0x01)))
	if err == nil {
		t.Fatal("expected control-character error")
	}
	if !strings.Contains(err.Error(), "control character") {
		t.Fatalf("error = %q, want control character context", err)
	}
}

func TestConvertToSQLUsesExplicitColumnsAndTSV(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("1\tAda\n2\tGrace\n"), &out, SQLConvertOptions{
		TableName: "people",
		Delimiter: '\t',
		Columns:   []string{"id", "name"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "INSERT INTO `people` (`id`, `name`) VALUES\n  ('1', 'Ada'),\n  ('2', 'Grace');\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestConvertToSQLBatchesRows(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("1,Ada\n2,Grace\n3,Linus\n"), &out, SQLConvertOptions{
		TableName:       "people",
		Columns:         []string{"id", "name"},
		InsertBatchSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "INSERT INTO `people` (`id`, `name`) VALUES\n  ('1', 'Ada'),\n  ('2', 'Grace');\nINSERT INTO `people` (`id`, `name`) VALUES\n  ('3', 'Linus');\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestConvertToSQLWritesCreateTableWithExplicitTypes(t *testing.T) {
	var out strings.Builder
	summary, err := ConvertToSQL(context.Background(), strings.NewReader("1,Ada\n"), &out, SQLConvertOptions{
		TableName:          "people",
		Columns:            []string{"id", "name"},
		IncludeCreateTable: true,
		ColumnTypes:        []string{SQLTypeBigInt, SQLTypeText},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE TABLE IF NOT EXISTS `people` (\n  `id` BIGINT,\n  `name` TEXT\n);\nINSERT INTO `people` (`id`, `name`) VALUES\n  ('1', 'Ada');\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if !summary.CreateTableWritten {
		t.Fatal("expected CREATE TABLE summary flag")
	}
}

func TestConvertToSQLPreservesExplicitTypesWithHeaderColumns(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("id,name\n1,Ada\n"), &out, SQLConvertOptions{
		TableName:          "people",
		HasHeader:          true,
		IncludeCreateTable: true,
		ColumnTypes:        []string{SQLTypeBigInt, SQLTypeText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "`id` BIGINT") || !strings.Contains(got, "`name` TEXT") {
		t.Fatalf("output = %q, want explicit header column types", got)
	}
}

func TestConvertToSQLFileInfersCreateTableSchema(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	output := filepath.Join(dir, "output.sql")
	if err := os.WriteFile(input, []byte("id,price,active,note\n1,12.5,true,hello\n2,14,false,NULL\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	summary, err := ConvertToSQLFile(context.Background(), input, output, SQLConvertOptions{
		TableName:          "items",
		HasHeader:          true,
		NullValues:         []string{"NULL"},
		IncludeCreateTable: true,
		InsertBatchSize:    2,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := "CREATE TABLE IF NOT EXISTS `items` (\n  `id` BIGINT,\n  `price` DOUBLE,\n  `active` BOOLEAN,\n  `note` TEXT\n);\n"
	if got := string(data); !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("output = %q, want CREATE TABLE prefix %q", got, wantPrefix)
	}
	if summary.Columns != 4 || summary.RowsWritten != 2 || !summary.CreateTableWritten {
		t.Fatalf("summary = %+v, want 4 columns, 2 rows, CREATE TABLE", summary)
	}
}

func TestSchemaColumnsForSQLSplitsNamesAndTypes(t *testing.T) {
	names, types := schemaColumnsForSQL([]SchemaColumn{
		{Name: "id", SQLType: SQLTypeBigInt},
		{Name: "active", SQLType: SQLTypeBoolean},
	})

	if got, want := strings.Join(names, ","), "id,active"; got != want {
		t.Fatalf("names = %q, want %q", got, want)
	}
	if got, want := strings.Join(types, ","), "BIGINT,BOOLEAN"; got != want {
		t.Fatalf("types = %q, want %q", got, want)
	}
}

func TestPreviewSQLConversionUsesBoundedRowsAndFormatsReport(t *testing.T) {
	report, err := PreviewSQLConversion(strings.NewReader("id,name\n1,Ada\n2,Grace\n3,Linus\n"), SQLPreviewOptions{
		SQLConvertOptions: SQLConvertOptions{
			TableName:       "people",
			HasHeader:       true,
			InsertBatchSize: 2,
		},
		MaxRows: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.RowsPreviewed != 2 || report.Summary.RowsWritten != 2 {
		t.Fatalf("report rows = preview %d written %d, want 2/2", report.RowsPreviewed, report.Summary.RowsWritten)
	}
	if !strings.Contains(report.SQL, "'Ada'") || !strings.Contains(report.SQL, "'Grace'") {
		t.Fatalf("preview SQL = %q, want first two data rows", report.SQL)
	}
	if strings.Contains(report.SQL, "Linus") {
		t.Fatalf("preview SQL included row beyond preview limit: %q", report.SQL)
	}
	formatted := FormatSQLPreviewReport(report)
	if !strings.Contains(formatted, "Rows previewed: 2") || !strings.Contains(formatted, "SQL preview:") || !strings.Contains(formatted, "preview limited to 2 data rows") {
		t.Fatalf("formatted report = %q, want summary, SQL, and row-limit warning", formatted)
	}
}

func TestPreviewSQLConversionInfersCreateTableFromSample(t *testing.T) {
	report, err := PreviewSQLConversion(strings.NewReader("id,price,active,note\n1,12.5,true,hello\n2,14,false,NULL\n"), SQLPreviewOptions{
		SQLConvertOptions: SQLConvertOptions{
			TableName:          "items",
			HasHeader:          true,
			NullValues:         []string{"NULL"},
			IncludeCreateTable: true,
		},
		MaxRows: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATE TABLE IF NOT EXISTS `items`", "`id` BIGINT", "`price` DOUBLE", "`active` BOOLEAN", "`note` TEXT"} {
		if !strings.Contains(report.SQL, want) {
			t.Fatalf("preview SQL = %q, want %q", report.SQL, want)
		}
	}
	if !report.Summary.CreateTableWritten {
		t.Fatal("expected preview summary to report CREATE TABLE output")
	}
}

func TestPreviewSQLConversionTrimsTrailingPartialRecord(t *testing.T) {
	report, err := PreviewSQLConversion(strings.NewReader("id,name\n1,Ada\n2,\"unterminated"), SQLPreviewOptions{
		SQLConvertOptions: SQLConvertOptions{
			TableName: "people",
			HasHeader: true,
		},
		MaxBytes: 19,
		MaxRows:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.TruncatedSample {
		t.Fatal("expected truncated sample")
	}
	if strings.Contains(report.SQL, "unterminated") {
		t.Fatalf("preview SQL included trailing partial record: %q", report.SQL)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), "trailing partial record omitted") {
		t.Fatalf("warnings = %#v, want partial-record warning", report.Warnings)
	}
}

func TestConvertToSQLNormalizesHeaderNames(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("id,,id\n1,Ada,2\n"), &out, SQLConvertOptions{
		TableName: "people",
		HasHeader: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "`id`, `column_2`, `id_2`") {
		t.Fatalf("output = %q, want normalized unique column names", got)
	}
}

func TestConvertToSQLRejectsMismatchedRecordWidth(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("1,Ada\n2\n"), &out, SQLConvertOptions{
		TableName: "people",
		Columns:   []string{"id", "name"},
	})
	if err == nil {
		t.Fatal("expected mismatched-width error")
	}
	if !strings.Contains(err.Error(), "record 2") {
		t.Fatalf("error = %q, want record context", err)
	}
}

func TestConvertToSQLRejectsUnsupportedDialect(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("1,Ada\n"), &out, SQLConvertOptions{
		TableName: "people",
		Columns:   []string{"id", "name"},
		Dialect:   SQLDialect("postgres"),
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported SQL dialect") {
		t.Fatalf("err = %v, want unsupported dialect", err)
	}
}

func TestConvertToSQLRejectsOversizedField(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("1,toolong\n"), &out, SQLConvertOptions{
		TableName:     "people",
		Columns:       []string{"id", "name"},
		MaxFieldBytes: 4,
	})
	if err == nil || !strings.Contains(err.Error(), "max field size") {
		t.Fatalf("err = %v, want max field size error", err)
	}
}

func TestConvertToSQLRejectsUnsafeIdentifiers(t *testing.T) {
	tests := []struct {
		name string
		opts SQLConvertOptions
		want string
	}{
		{
			name: "table control",
			opts: SQLConvertOptions{
				TableName: "bad\nname",
				Columns:   []string{"id"},
			},
			want: "table name",
		},
		{
			name: "explicit column control",
			opts: SQLConvertOptions{
				TableName: "people",
				Columns:   []string{"bad\tcolumn"},
			},
			want: "column 1",
		},
		{
			name: "header column control",
			opts: SQLConvertOptions{
				TableName: "people",
				HasHeader: true,
			},
			want: "column 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var input string
			if tt.opts.HasHeader {
				input = "\"bad\tcolumn\"\n1\n"
			} else {
				input = "1\n"
			}
			var out strings.Builder
			_, err := ConvertToSQL(context.Background(), strings.NewReader(input), &out, tt.opts)
			if err == nil {
				t.Fatal("expected unsafe identifier error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
			if out.Len() != 0 {
				t.Fatalf("wrote output before rejecting identifier: %q", out.String())
			}
		})
	}
}

func TestConvertToSQLStripsUTF8BOM(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("\xEF\xBB\xBFid,name\n1,alice\n"), &out, SQLConvertOptions{
		TableName: "users",
		HasHeader: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sql := out.String()
	if strings.Contains(sql, "FEFF") {
		t.Fatalf("BOM leaked into output: %q", sql)
	}
	if !strings.Contains(sql, "`id`") {
		t.Fatalf("first column should be `id`, got: %q", sql)
	}
}

func TestConvertToSQLRejectsUTF16Input(t *testing.T) {
	// UTF-16LE BOM + "id\n" interleaved with NULs.
	in := "\xFF\xFEi\x00d\x00\n\x00"
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader(in), &out, SQLConvertOptions{
		TableName: "t",
		HasHeader: true,
	})
	if !errors.Is(err, ErrUTF16Input) {
		t.Fatalf("err = %v, want ErrUTF16Input", err)
	}
}

func TestConvertToSQLTrimsLeadingSpaceForNullMatch(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("1, NULL\n"), &out, SQLConvertOptions{
		TableName:  "t",
		Columns:    []string{"a", "b"},
		NullValues: []string{"NULL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "'1', NULL)") {
		t.Fatalf("unquoted ' NULL' should match the NULL set and emit NULL, got: %q", out.String())
	}
}

func TestConvertToSQLKeepsQuotedLeadingSpace(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("1,\" NULL\"\n"), &out, SQLConvertOptions{
		TableName:  "t",
		Columns:    []string{"a", "b"},
		NullValues: []string{"NULL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "' NULL'") {
		t.Fatalf("quoted \" NULL\" should stay a string, got: %q", out.String())
	}
}

func TestConvertToSQLReplacePolicySanitizes(t *testing.T) {
	var out strings.Builder
	summary, err := ConvertToSQL(context.Background(), strings.NewReader("1,bad\x01value\n"), &out, SQLConvertOptions{
		TableName:      "t",
		Columns:        []string{"id", "note"},
		OnInvalidValue: InvalidValueReplace,
	})
	if err != nil {
		t.Fatalf("replace policy should not error: %v", err)
	}
	if summary.SanitizedRows != 1 {
		t.Fatalf("SanitizedRows = %d, want 1", summary.SanitizedRows)
	}
	if summary.RowsWritten != 1 {
		t.Fatalf("RowsWritten = %d, want 1", summary.RowsWritten)
	}
	if strings.ContainsRune(out.String(), 0x01) {
		t.Fatalf("control byte leaked into output: %q", out.String())
	}
}

func TestConvertToSQLSkipRowPolicyDropsBadRecord(t *testing.T) {
	var out strings.Builder
	summary, err := ConvertToSQL(context.Background(), strings.NewReader("1,ok\n2,bad\x01value\n3,fine\n"), &out, SQLConvertOptions{
		TableName:      "t",
		Columns:        []string{"id", "note"},
		OnInvalidValue: InvalidValueSkipRow,
	})
	if err != nil {
		t.Fatalf("skip-row policy should not error: %v", err)
	}
	if summary.SkippedRows != 1 {
		t.Fatalf("SkippedRows = %d, want 1", summary.SkippedRows)
	}
	if summary.RowsWritten != 2 {
		t.Fatalf("RowsWritten = %d, want 2 (rows 1 and 3)", summary.RowsWritten)
	}
}

func TestConvertToSQLFileKeepsPartialOutputOnDataError(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	output := filepath.Join(dir, "output.sql")
	// Rows 1-2 are clean; row 3 has a control byte. Default policy (fail) aborts
	// at row 3, but the already-written rows must be retained.
	if err := os.WriteFile(input, []byte("1,ok\n2,fine\n3,bad\x01value\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	_, err := ConvertToSQLFile(context.Background(), input, output, SQLConvertOptions{
		TableName:       "t",
		Columns:         []string{"id", "note"},
		InsertBatchSize: 1,
	})
	if err == nil {
		t.Fatal("expected a data error on row 3")
	}
	data, statErr := os.ReadFile(output)
	if statErr != nil {
		t.Fatalf("partial output should be retained on a data error: %v", statErr)
	}
	if !strings.Contains(string(data), "'ok'") {
		t.Fatalf("partial output should contain the first written row, got: %q", string(data))
	}
}

func TestConvertToSQLRejectsUnsafeFieldControlCharacters(t *testing.T) {
	var out strings.Builder
	_, err := ConvertToSQL(context.Background(), strings.NewReader("1,bad\x01value\n"), &out, SQLConvertOptions{
		TableName: "people",
		Columns:   []string{"id", "note"},
	})
	if err == nil {
		t.Fatal("expected unsafe field error")
	}
	if !strings.Contains(err.Error(), "record 1") || !strings.Contains(err.Error(), "field 2") {
		t.Fatalf("error = %q, want record and field context", err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote output before rejecting unsafe field: %q", out.String())
	}
}

func TestConvertToSQLFileDeletesPartialOutputOnCancel(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	output := filepath.Join(dir, "output.sql")
	if err := os.WriteFile(input, []byte("id,name\n1,Ada\n2,Grace\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, err := ConvertToSQLFile(ctx, input, output, SQLConvertOptions{
		TableName:       "people",
		HasHeader:       true,
		InsertBatchSize: 1,
		Progress: func(SQLConvertProgress) {
			cancel()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial output still exists: %v", statErr)
	}
}

func TestConvertToSQLFileRejectsSamePathAndExistingOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	output := filepath.Join(dir, "output.sql")
	if err := os.WriteFile(input, []byte("id,name\n1,Ada\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := ConvertToSQLFile(context.Background(), input, input, SQLConvertOptions{TableName: "people", HasHeader: true}); err == nil {
		t.Fatal("expected same-path error")
	}
	if err := os.WriteFile(output, []byte("preexisting"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := ConvertToSQLFile(context.Background(), input, output, SQLConvertOptions{TableName: "people", HasHeader: true}); err == nil {
		t.Fatal("expected existing-output error")
	}
	if data, err := os.ReadFile(output); err != nil || string(data) != "preexisting" {
		t.Fatalf("preexisting output changed, data=%q err=%v", data, err)
	}
}
