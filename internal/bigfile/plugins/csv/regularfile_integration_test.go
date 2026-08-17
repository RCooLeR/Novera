package csv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/regularfile"
)

func TestCSVFileOperationsRejectDirectoryInputBeforeOutput(t *testing.T) {
	inputDirectory := t.TempDir()
	outputDirectory := t.TempDir()

	tests := []struct {
		name string
		run  func(string) error
	}{
		{
			name: "JSONL export",
			run: func(output string) error {
				_, err := ExportJSONLFile(context.Background(), inputDirectory, output, JSONLOptions{})
				return err
			},
		},
		{
			name: "projection",
			run: func(output string) error {
				_, err := ProjectColumnsFile(context.Background(), inputDirectory, output, ProjectOptions{Columns: []int{0}})
				return err
			},
		},
		{
			name: "filter",
			run: func(output string) error {
				_, err := FilterRowsFile(context.Background(), inputDirectory, output, FilterOptions{
					Column: 0,
					Op:     "nonempty",
				})
				return err
			},
		},
		{
			name: "dedupe",
			run: func(output string) error {
				_, err := DedupeRowsFile(context.Background(), inputDirectory, output, DedupeOptions{KeyColumn: -1})
				return err
			},
		},
		{
			name: "sample",
			run: func(output string) error {
				_, err := SampleRowsFile(context.Background(), inputDirectory, output, SampleOptions{})
				return err
			},
		},
		{
			name: "redaction",
			run: func(output string) error {
				_, err := RedactColumnsFile(context.Background(), inputDirectory, output, RedactOptions{
					Columns: map[int]RedactMode{0: RedactNull},
				})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(outputDirectory, test.name+".out")
			err := test.run(output)
			if !errors.Is(err, regularfile.ErrNotRegular) {
				t.Fatalf("error = %v, want regularfile.ErrNotRegular", err)
			}
			for _, path := range []string{output, tempOutputPath(output)} {
				if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("unexpected output %q after rejected input: %v", path, statErr)
				}
			}
		})
	}
}
