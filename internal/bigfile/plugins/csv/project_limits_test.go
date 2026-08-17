package csv

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestProjectTransformConfigLimits(t *testing.T) {
	t.Run("accepts maximum column mapping count", func(t *testing.T) {
		input := strings.Repeat("x,", MaxTransformColumnMappings-1) + "x\n"
		reader := &countReadsReader{reader: strings.NewReader(input)}

		if _, err := ProjectColumns(context.Background(), reader, io.Discard, ProjectOptions{
			Columns: sequentialColumnIndexes(MaxTransformColumnMappings),
		}); err != nil {
			t.Fatalf("ProjectColumns() error = %v", err)
		}
		if reader.reads == 0 {
			t.Fatal("ProjectColumns() did not read accepted input")
		}
	})

	t.Run("rejects excessive mappings before input", func(t *testing.T) {
		reader := &countReadsReader{reader: strings.NewReader("x\n")}

		_, err := ProjectColumns(context.Background(), reader, io.Discard, ProjectOptions{
			Columns: sequentialColumnIndexes(MaxTransformColumnMappings + 1),
		})
		if err == nil {
			t.Fatal("ProjectColumns() error = nil, want mapping limit error")
		}
		if reader.reads != 0 {
			t.Fatalf("input reads = %d, want 0", reader.reads)
		}
	})

	t.Run("rejects negative and duplicate mappings before input", func(t *testing.T) {
		for name, columns := range map[string][]int{
			"negative":  {0, -1},
			"duplicate": {0, 0},
		} {
			t.Run(name, func(t *testing.T) {
				reader := &countReadsReader{reader: strings.NewReader("x\n")}

				if _, err := ProjectColumns(context.Background(), reader, io.Discard, ProjectOptions{
					Columns: columns,
				}); err == nil {
					t.Fatal("ProjectColumns() error = nil, want column validation error")
				}
				if reader.reads != 0 {
					t.Fatalf("input reads = %d, want 0", reader.reads)
				}
			})
		}
	})

	t.Run("accepts maximum configuration string bytes", func(t *testing.T) {
		reader := &countReadsReader{reader: strings.NewReader("x\n")}

		if _, err := ProjectColumns(context.Background(), reader, io.Discard, ProjectOptions{
			Columns:      []int{0},
			MissingValue: strings.Repeat("x", MaxTransformConfigStringBytes),
		}); err != nil {
			t.Fatalf("ProjectColumns() error = %v", err)
		}
		if reader.reads == 0 {
			t.Fatal("ProjectColumns() did not read accepted input")
		}
	})

	t.Run("rejects excessive configuration string bytes before input", func(t *testing.T) {
		reader := &countReadsReader{reader: strings.NewReader("x\n")}

		_, err := ProjectColumns(context.Background(), reader, io.Discard, ProjectOptions{
			Columns:      []int{0},
			MissingValue: strings.Repeat("x", MaxTransformConfigStringBytes+1),
		})
		if err == nil {
			t.Fatal("ProjectColumns() error = nil, want configuration string limit error")
		}
		if reader.reads != 0 {
			t.Fatalf("input reads = %d, want 0", reader.reads)
		}
	})

	t.Run("rejects combined missing-value expansion before input", func(t *testing.T) {
		reader := &countReadsReader{reader: strings.NewReader("x\n")}

		_, err := ProjectColumns(context.Background(), reader, io.Discard, ProjectOptions{
			Columns:           sequentialColumnIndexes(MaxTransformColumnMappings),
			AllowShortRecords: true,
			MissingValue:      strings.Repeat("x", MaxTransformConfigStringBytes),
		})
		if err == nil || !strings.Contains(err.Error(), "output-record limit") {
			t.Fatalf("ProjectColumns() error = %v, want output-record limit", err)
		}
		if reader.reads != 0 {
			t.Fatalf("input reads = %d, want 0", reader.reads)
		}
	})
}

func TestProjectRejectsExpandedOutputRecord(t *testing.T) {
	input := strings.Repeat("x", int(MaxLogicalRecordBytes)-1) + "\n"

	_, err := ProjectColumns(context.Background(), strings.NewReader(input), io.Discard, ProjectOptions{
		Columns:           []int{0, 1},
		AllowShortRecords: true,
		MissingValue:      "x",
	})
	if err == nil || !strings.Contains(err.Error(), "output-record limit") {
		t.Fatalf("ProjectColumns() error = %v, want output-record limit", err)
	}
}

func sequentialColumnIndexes(count int) []int {
	indexes := make([]int, count)
	for i := range indexes {
		indexes[i] = i
	}
	return indexes
}
