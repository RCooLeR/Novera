package bigfile

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/plugins/csv"
)

func validCSVSQLConfig() CsvSqlConfig {
	return CsvSqlConfig{
		Delimiter: ",", TableName: "records", InsertMode: "insert",
		Columns: []CsvSqlColumnConfig{{
			Source: 0, Name: "id", Type: csv.SQLTypeText, Include: true,
		}},
	}
}

func TestCSVSQLServiceInsertModesAreExact(t *testing.T) {
	valid := map[string]csv.SQLInsertMode{
		"insert":  csv.SQLInsertModeInsert,
		"ignore":  csv.SQLInsertModeInsertIgnore,
		"replace": csv.SQLInsertModeReplace,
	}
	for mode, want := range valid {
		cfg := validCSVSQLConfig()
		cfg.InsertMode = mode
		opts, err := csvSqlOptions(cfg)
		if err != nil || opts.InsertMode != want {
			t.Fatalf("mode %q = %q, %v; want %q", mode, opts.InsertMode, err, want)
		}
	}
	for _, mode := range []string{"", "INSERT", " insert", "insert ", "upsert"} {
		cfg := validCSVSQLConfig()
		cfg.InsertMode = mode
		if _, err := csvSqlOptions(cfg); err == nil || !strings.Contains(err.Error(), "insert mode") {
			t.Fatalf("mode %q error = %v, want exact-mode rejection", mode, err)
		}
	}
}

func TestCSVSQLServiceRejectsInvalidRequestsBeforeServiceAccess(t *testing.T) {
	var service *FileService
	for _, delimiter := range []string{"", "||"} {
		if _, err := service.CsvToSQLViaDialog("missing", 1, delimiter, "records", true, false); err == nil {
			t.Fatalf("invalid delimiter %q reached service access", delimiter)
		}
	}
	if _, err := service.CsvToSQLViaDialog("missing", 1, ",", strings.Repeat("x", csv.MaxSQLIdentifierBytes+1), true, false); err == nil {
		t.Fatal("oversized table name reached service access")
	}

	cfg := validCSVSQLConfig()
	cfg.BatchSize = math.MaxInt
	if _, err := service.CsvToSQLConfigViaDialog("missing", 1, cfg); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("batch error = %v, want pre-service limit rejection", err)
	}
	cfg = validCSVSQLConfig()
	cfg.Columns[0].Type = "BOOLISH"
	if _, err := service.CsvToSQLConfigViaDialog("missing", 1, cfg); err == nil || !strings.Contains(err.Error(), "invalid SQL column type") {
		t.Fatalf("type error = %v, want pre-service enum rejection", err)
	}
	cfg = validCSVSQLConfig()
	cfg.OnInvalid = " fail "
	if _, err := service.CsvToSQLConfigViaDialog("missing", 1, cfg); err == nil || !strings.Contains(err.Error(), "invalid-value policy") {
		t.Fatalf("policy error = %v, want exact pre-service enum rejection", err)
	}
}

func TestCSVSQLServiceBoundsCollectionsBeforeAllocation(t *testing.T) {
	cfg := validCSVSQLConfig()
	cfg.Columns = make([]CsvSqlColumnConfig, csv.MaxTransformColumnMappings+1)
	if _, err := csvSqlOptions(cfg); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("oversized column mapping error = %v", err)
	}

	cfg = validCSVSQLConfig()
	cfg.Columns = []CsvSqlColumnConfig{
		{Source: 0, Name: "id", Type: csv.SQLTypeText, Include: true},
		{Source: 0, Name: "copy", Type: csv.SQLTypeText, Include: true},
	}
	if _, err := csvSqlOptions(cfg); err == nil || !strings.Contains(err.Error(), "selected more than once") {
		t.Fatalf("duplicate source error = %v", err)
	}

	cfg = validCSVSQLConfig()
	cfg.NullValues = []string{strings.Repeat("x", csv.MaxTransformConfigStringBytes)}
	if _, err := csvSqlOptions(cfg); err == nil || !strings.Contains(err.Error(), "aggregate limit") {
		t.Fatalf("aggregate string error = %v", err)
	}
}

func TestConvertCSVToSQLVerifiedPublishesOnlyCompleteOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "input.csv")
	outputPath := filepath.Join(dir, "output.sql")
	if err := os.WriteFile(sourcePath, []byte("id,name\n1,Ada\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	summary, err := convertCSVToSQLVerified(context.Background(), doc, sourcePath, outputPath, csv.SQLConvertOptions{
		Delimiter: ',', TableName: "people", HasHeader: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RowsWritten != 1 {
		t.Fatalf("summary = %+v, want one output row", summary)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "INSERT INTO `people`") || !strings.Contains(string(data), "'Ada'") {
		t.Fatalf("published SQL = %q", data)
	}
}

func TestConvertCSVToSQLVerifiedRefusesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "input.csv")
	outputPath := filepath.Join(dir, "output.sql")
	source := []byte("id,name\n1,Ada\n")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	_, err = convertCSVToSQLVerified(context.Background(), doc, sourcePath, outputPath, csv.SQLConvertOptions{
		Delimiter: ',', TableName: "people", HasHeader: true,
	}, nil)
	if err == nil {
		t.Fatal("existing output was overwritten")
	}
	if data, readErr := os.ReadFile(outputPath); readErr != nil || string(data) != "keep" {
		t.Fatalf("existing output changed: data=%q err=%v", data, readErr)
	}
	if data, readErr := os.ReadFile(sourcePath); readErr != nil || string(data) != string(source) {
		t.Fatalf("source changed: data=%q err=%v", data, readErr)
	}
}

func TestConvertCSVToSQLVerifiedRejectsSourceMutationBeforePublication(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "input.csv")
	outputPath := filepath.Join(dir, "output.sql")
	source := []byte("id,name\n1,Ada\n2,Grace\n")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	mutated := false
	_, err = convertCSVToSQLVerified(context.Background(), doc, sourcePath, outputPath, csv.SQLConvertOptions{
		Delimiter: ',', TableName: "people", HasHeader: true, InsertBatchSize: 1,
		Progress: func(csv.SQLConvertProgress) {
			if mutated {
				return
			}
			mutated = true
			replacement := append([]byte(nil), source...)
			copy(replacement[len(replacement)-len("Grace\n"):], []byte("Other\n"))
			if writeErr := os.WriteFile(sourcePath, replacement, 0o600); writeErr != nil {
				t.Errorf("mutate source: %v", writeErr)
			}
		},
	}, nil)
	if err == nil || !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("error = %v, want ErrOutputSourceChanged", err)
	}
	if _, statErr := os.Stat(outputPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("mutated source published output: %v", statErr)
	}
}
