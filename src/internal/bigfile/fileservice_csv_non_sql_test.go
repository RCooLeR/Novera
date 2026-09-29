package bigfile

import (
	"context"
	stdcsv "encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"novera/internal/bigfile/fileio"
	plugincsv "novera/internal/bigfile/plugins/csv"
)

func installNonSQLCSVSaveDialog(t *testing.T, dialog func(string, string) (string, error)) {
	t.Helper()
	previous := csvTransformSaveDialog
	csvTransformSaveDialog = dialog
	t.Cleanup(func() { csvTransformSaveDialog = previous })
}

func TestCSVTransformsValidateBeforeSaveDialog(t *testing.T) {
	dialogCalls := 0
	installNonSQLCSVSaveDialog(t, func(string, string) (string, error) {
		dialogCalls++
		return filepath.Join(t.TempDir(), "unexpected.csv"), nil
	})
	service := NewFileService()
	tests := []struct {
		name      string
		call      func() error
		errorPart string
	}{
		{
			name: "project duplicate index",
			call: func() error {
				_, err := service.CsvProjectViaDialog("missing", 1, ",", []int{0, 0})
				return err
			},
			errorPart: "duplicates",
		},
		{
			name: "add column position",
			call: func() error {
				_, err := service.CsvAddColumnViaDialog("missing", 1, ",", plugincsv.MaxAddColumnPosition+1, "x")
				return err
			},
			errorPart: "exceeds maximum",
		},
		{
			name: "redact duplicate index",
			call: func() error {
				_, err := service.CsvRedactViaDialog("missing", 1, ",", true, []CsvRedactColumn{
					{Index: 1, Mode: "null"}, {Index: 1, Mode: "fixed"},
				}, "")
				return err
			},
			errorPart: "selected more than once",
		},
		{
			name: "redact mode exact",
			call: func() error {
				_, err := service.CsvRedactViaDialog("missing", 1, ",", true, []CsvRedactColumn{
					{Index: 1, Mode: " hash "},
				}, "")
				return err
			},
			errorPart: "invalid redaction mode",
		},
		{
			name: "filter operation exact",
			call: func() error {
				_, err := service.CsvFilterViaDialog("missing", 1, ",", true, 0, " eq ", "x", false)
				return err
			},
			errorPart: "invalid filter operation",
		},
		{
			name: "dedupe delimiter",
			call: func() error {
				_, err := service.CsvDedupeViaDialog("missing", 1, "||", true, 0)
				return err
			},
			errorPart: "invalid delimiter",
		},
		{
			name: "sample delimiter",
			call: func() error {
				_, err := service.CsvSampleViaDialog("missing", 1, "||", true, 10)
				return err
			},
			errorPart: "invalid delimiter",
		},
		{
			name: "JSONL delimiter",
			call: func() error {
				_, err := service.CsvExportJSONLViaDialog("missing", 1, "||", true, false)
				return err
			},
			errorPart: "invalid delimiter",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil || !strings.Contains(err.Error(), test.errorPart) {
				t.Fatalf("error = %v, want substring %q", err, test.errorPart)
			}
		})
	}
	if dialogCalls != 0 {
		t.Fatalf("invalid CSV requests opened save dialog %d times", dialogCalls)
	}
}

func TestCSVAddColumnServicePreservesFieldsBeyondInsertion(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.csv")
	destination := filepath.Join(dir, "added.csv")
	if err := os.WriteFile(source, []byte("a,b,c\n1,2,3\nshort\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	installNonSQLCSVSaveDialog(t, func(string, string) (string, error) { return destination, nil })

	generation := csvServiceGeneration(t, service, meta.FileID)
	result, err := service.CsvAddColumnViaDialog(meta.FileID, generation, ",", 1, "NEW")
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordsRead != 3 || result.RecordsWritten != 3 {
		t.Fatalf("result = %+v", result)
	}
	got := readCSVServiceRows(t, destination)
	want := [][]string{
		{"a", "NEW", "b", "c"},
		{"1", "NEW", "2", "3"},
		{"short", "NEW"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %#v, want %#v", got, want)
	}
}

func TestCSVRedactionServiceUsesFreshOperationKeys(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.csv")
	if err := os.WriteFile(source, []byte("id,secret\n1,alice\n2,alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)

	currentDestination := filepath.Join(dir, "first.csv")
	installNonSQLCSVSaveDialog(t, func(string, string) (string, error) { return currentDestination, nil })
	columns := []CsvRedactColumn{{Index: 1, Mode: "hash"}}
	generation := csvServiceGeneration(t, service, meta.FileID)
	if _, err := service.CsvRedactViaDialog(meta.FileID, generation, ",", true, columns, ""); err != nil {
		t.Fatal(err)
	}
	first := readCSVServiceRows(t, currentDestination)
	currentDestination = filepath.Join(dir, "second.csv")
	if _, err := service.CsvRedactViaDialog(meta.FileID, generation, ",", true, columns, ""); err != nil {
		t.Fatal(err)
	}
	second := readCSVServiceRows(t, currentDestination)
	if first[1][1] != first[2][1] || second[1][1] != second[2][1] {
		t.Fatalf("equal values were not stable within an operation: %#v / %#v", first, second)
	}
	if len(first[1][1]) != 32 || len(second[1][1]) != 32 {
		t.Fatalf("pseudonym lengths = %d/%d, want 32 hex characters", len(first[1][1]), len(second[1][1]))
	}
	if first[1][1] == second[1][1] {
		t.Fatal("separate redaction operations produced linkable pseudonyms")
	}
}

func TestRunCSVTransformVerifiedRejectsChangedSourceWithoutPublishing(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.csv")
	destination := filepath.Join(dir, "output.csv")
	original := []byte("id,value\n1,alpha\n")
	changed := []byte("id,value\n1,omega\n")
	if len(original) != len(changed) {
		t.Fatal("test source mutations must have equal size")
	}
	if err := os.WriteFile(source, original, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	file, ok := service.reg.Get(meta.FileID)
	if !ok {
		t.Fatal("opened file disappeared")
	}
	defer file.Release()

	_, err = runCSVTransformVerified(context.Background(), file.Doc, file.Path, destination, nil,
		func(_ context.Context, r io.Reader, w io.Writer) (int64, error) {
			written, copyErr := io.Copy(w, r)
			if copyErr != nil {
				return written, copyErr
			}
			if err := os.WriteFile(source, changed, 0o600); err != nil {
				return written, err
			}
			return written, nil
		},
	)
	if !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("error = %v, want ErrOutputSourceChanged", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed source published output: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "source.csv" {
		t.Fatalf("failed verified transform left artifacts: %v", entries)
	}
}

func TestCSVServicePreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.csv")
	destination := filepath.Join(dir, "existing.csv")
	if err := os.WriteFile(source, []byte("id,value\n1,alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	installNonSQLCSVSaveDialog(t, func(string, string) (string, error) { return destination, nil })

	generation := csvServiceGeneration(t, service, meta.FileID)
	if _, err := service.CsvSampleViaDialog(meta.FileID, generation, ",", true, 2); !errors.Is(err, fileio.ErrExists) {
		t.Fatalf("error = %v, want fileio.ErrExists", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sentinel" {
		t.Fatalf("existing destination changed: %q", data)
	}
}

func csvServiceGeneration(t *testing.T, service *FileService, fileID string) uint64 {
	t.Helper()
	inspection, err := service.CsvInspect(fileID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Generation == 0 {
		t.Fatal("CSV inspection returned an empty generation")
	}
	return inspection.Generation
}

func readCSVServiceRows(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := stdcsv.NewReader(file)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
