package csv

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const integrationRecordLimit int64 = 32

func oversizedMultilineCSV() string {
	return "a,b\n1,\"" + strings.Repeat("x", 20) + "\n" + strings.Repeat("y", 20) + "\"\n"
}

func TestCSVReaderOperationsEnforceLogicalRecordLimit(t *testing.T) {
	input := oversizedMultilineCSV()
	ctx := context.Background()
	tests := []struct {
		name string
		run  func(io.Reader) error
	}{
		{"inspect", func(r io.Reader) error {
			_, err := InspectReaderContext(ctx, r, InspectOptions{
				MaxBytes: 1024, MaxRows: 10, MaxRecordBytes: integrationRecordLimit,
				Delimiters: []rune{','},
			})
			return err
		}},
		{"schema", func(r io.Reader) error {
			_, err := InferSchemaContext(ctx, r, SchemaOptions{
				Delimiter: ',', HasHeader: true, MaxBytes: 1024, MaxRows: 10,
				MaxRecordBytes: integrationRecordLimit,
			})
			return err
		}},
		{"profile", func(r io.Reader) error {
			_, err := ProfileColumns(ctx, r, SchemaOptions{
				Delimiter: ',', HasHeader: true, MaxBytes: 1024, MaxRows: 10,
				MaxRecordBytes: integrationRecordLimit,
			})
			return err
		}},
		{"preview", func(r io.Reader) error {
			_, err := PreviewRowsContext(ctx, r, PreviewOptions{
				Delimiter: ',', HasHeader: true, MaxBytes: 1024, MaxRows: 10,
				MaxRecordBytes: integrationRecordLimit,
			})
			return err
		}},
		{"column guide", func(r io.Reader) error {
			_, err := BuildColumnGuideContext(ctx, r, ColumnGuideOptions{
				Delimiter: ',', HasHeader: true, MaxBytes: 1024, MaxRows: 10,
				MaxRecordBytes: integrationRecordLimit,
			})
			return err
		}},
		{"project preview", func(r io.Reader) error {
			_, err := PreviewProjectedColumnsContext(ctx, r, ProjectPreviewOptions{
				Delimiter: ',', Columns: []int{0}, MaxBytes: 1024, MaxRows: 10,
				MaxRecordBytes: integrationRecordLimit,
			})
			return err
		}},
		{"project", func(r io.Reader) error {
			_, err := ProjectColumns(ctx, r, io.Discard, ProjectOptions{
				Delimiter: ',', Columns: []int{0}, MaxRecordBytes: integrationRecordLimit,
			})
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run(strings.NewReader(input))
			if !errors.Is(err, ErrCSVRecordTooLarge) {
				t.Fatalf("error = %v, want ErrCSVRecordTooLarge", err)
			}
			var limitErr *RecordLimitError
			if !errors.As(err, &limitErr) {
				t.Fatalf("error type = %T, want *RecordLimitError", err)
			}
			if limitErr.Record != 2 || limitErr.StartOffset != 4 || limitErr.LimitBytes != integrationRecordLimit {
				t.Fatalf("limit error = %+v", limitErr)
			}
		})
	}
}

func TestCSVReaderOperationsEnforceFieldLimit(t *testing.T) {
	input := "ok\n" + strings.Repeat(",", MaxCSVFieldsPerRecord) + "\n"
	ctx := context.Background()
	tests := []struct {
		name string
		run  func(io.Reader) error
	}{
		{"inspect", func(r io.Reader) error {
			_, err := InspectReaderContext(ctx, r, InspectOptions{MaxBytes: int64(len(input)), MaxRows: 10, Delimiters: []rune{','}})
			return err
		}},
		{"schema", func(r io.Reader) error {
			_, err := InferSchemaContext(ctx, r, SchemaOptions{Delimiter: ',', HasHeader: true, MaxBytes: int64(len(input)), MaxRows: 10})
			return err
		}},
		{"profile", func(r io.Reader) error {
			_, err := ProfileColumns(ctx, r, SchemaOptions{Delimiter: ',', HasHeader: true, MaxBytes: int64(len(input)), MaxRows: 10})
			return err
		}},
		{"preview", func(r io.Reader) error {
			_, err := PreviewRowsContext(ctx, r, PreviewOptions{Delimiter: ',', HasHeader: true, MaxBytes: int64(len(input)), MaxRows: 10})
			return err
		}},
		{"project", func(r io.Reader) error {
			_, err := ProjectColumns(ctx, r, io.Discard, ProjectOptions{Delimiter: ',', Columns: []int{0}})
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run(strings.NewReader(input))
			assertFieldLimitError(t, err, 2, 3, MaxCSVFieldsPerRecord+1)
		})
	}
}

func TestCSVFileOperationsDoNotPublishOnRecordLimit(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		run  func(string, string) error
	}{
		{"project", func(src, dst string) error {
			_, err := ProjectColumnsFile(ctx, src, dst, ProjectOptions{Delimiter: ',', Columns: []int{0}, MaxRecordBytes: integrationRecordLimit})
			return err
		}},
		{"filter", func(src, dst string) error {
			_, err := FilterRowsFile(ctx, src, dst, FilterOptions{Delimiter: ',', HasHeader: true, Column: 0, Op: "nonempty", MaxRecordBytes: integrationRecordLimit})
			return err
		}},
		{"dedupe", func(src, dst string) error {
			_, err := DedupeRowsFile(ctx, src, dst, DedupeOptions{Delimiter: ',', HasHeader: true, KeyColumn: -1, MaxRecordBytes: integrationRecordLimit})
			return err
		}},
		{"sample", func(src, dst string) error {
			_, err := SampleRowsFile(ctx, src, dst, SampleOptions{Delimiter: ',', HasHeader: true, EveryN: 1, MaxRecordBytes: integrationRecordLimit})
			return err
		}},
		{"redact", func(src, dst string) error {
			_, err := RedactColumnsFile(ctx, src, dst, RedactOptions{
				Delimiter: ',', HasHeader: true, Columns: map[int]RedactMode{0: RedactFixed},
				MaxRecordBytes: integrationRecordLimit,
			})
			return err
		}},
		{"JSONL", func(src, dst string) error {
			_, err := ExportJSONLFile(ctx, src, dst, JSONLOptions{Delimiter: ',', HasHeader: true, MaxRecordBytes: integrationRecordLimit})
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "source.csv")
			dst := filepath.Join(dir, "output.dat")
			if err := os.WriteFile(src, []byte(oversizedMultilineCSV()), 0o600); err != nil {
				t.Fatal(err)
			}
			err := test.run(src, dst)
			if !errors.Is(err, ErrCSVRecordTooLarge) {
				t.Fatalf("error = %v, want ErrCSVRecordTooLarge", err)
			}
			if _, statErr := os.Lstat(dst); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("final output exists after failed transform: %v", statErr)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 1 || entries[0].Name() != "source.csv" {
				t.Fatalf("temporary output was not cleaned up: %v", entries)
			}
		})
	}
}
