package csv

import (
	"context"
	stdcsv "encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAddColumnRecordPreservesAllFields(t *testing.T) {
	tests := []struct {
		name     string
		record   []string
		position int
		want     []string
	}{
		{name: "empty record", record: nil, position: 0, want: []string{"NEW"}},
		{name: "prepend", record: []string{"a"}, position: 0, want: []string{"NEW", "a"}},
		{name: "append", record: []string{"a"}, position: 1, want: []string{"a", "NEW"}},
		{name: "middle", record: []string{"a", "b", "c"}, position: 1, want: []string{"a", "NEW", "b", "c"}},
		{name: "pad short row", record: []string{"a"}, position: 3, want: []string{"a", "FILL", "FILL", "NEW"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := append([]string(nil), test.record...)
			got := addColumnRecord(test.record, test.position, "NEW", "FILL")
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("record = %#v, want %#v", got, test.want)
			}
			if !reflect.DeepEqual(test.record, original) {
				t.Fatalf("input record mutated: got %#v want %#v", test.record, original)
			}
		})
	}
}

func TestAddColumnPreservesRaggedAndWideRows(t *testing.T) {
	input := ",id,id\n1\n2,a,b,c\n"
	var output strings.Builder
	summary, err := AddColumn(context.Background(), strings.NewReader(input), &output, AddColumnOptions{
		Delimiter: ',', Position: 3, Value: "new\nvalue", FillValue: "FILL",
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.RecordsRead != 3 || summary.RecordsWritten != 3 || summary.Position != 3 {
		t.Fatalf("summary = %+v", summary)
	}
	reader := stdcsv.NewReader(strings.NewReader(output.String()))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"", "id", "id", "new\nvalue"},
		{"1", "FILL", "FILL", "new\nvalue"},
		{"2", "a", "b", "new\nvalue", "c"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %#v, want %#v", rows, want)
	}
}

func TestAddColumnEnforcesWidthAndCleansFailure(t *testing.T) {
	input := strings.TrimSuffix(strings.Repeat("x,", MaxCSVFieldsPerRecord), ",") + "\n"
	var output strings.Builder
	summary, err := AddColumn(context.Background(), strings.NewReader(input), &output, AddColumnOptions{
		Delimiter: ',', Position: 0, Value: "new",
	})
	if err == nil || !strings.Contains(err.Error(), "would contain") {
		t.Fatalf("error = %v, want field-limit refusal", err)
	}
	if summary.RecordsRead != 1 || summary.RecordsWritten != 0 || output.Len() != 0 {
		t.Fatalf("summary/output = %+v/%q", summary, output.String())
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "input.csv")
	dst := filepath.Join(dir, "output.csv")
	if err := os.WriteFile(src, []byte("a,b\n1,\"unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AddColumnFile(context.Background(), src, dst, AddColumnOptions{
		Delimiter: ',', Position: 1, Value: "x",
	}); err == nil {
		t.Fatal("expected malformed CSV error")
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed add-column published output: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "input.csv" {
		t.Fatalf("temporary artifacts remain: %v", entries)
	}
}

func TestAddColumnRejectsInvalidPositionBeforeArtifacts(t *testing.T) {
	for _, position := range []int{-1, MaxAddColumnPosition + 1} {
		dir := t.TempDir()
		output := filepath.Join(dir, "output.csv")
		_, err := AddColumnFile(context.Background(), filepath.Join(dir, "missing.csv"), output, AddColumnOptions{
			Delimiter: ',', Position: position, Value: "x",
		})
		if err == nil || !strings.Contains(err.Error(), "position") {
			t.Fatalf("position %d error = %v", position, err)
		}
		if _, statErr := os.Lstat(output); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("invalid position created output: %v", statErr)
		}
	}
}
