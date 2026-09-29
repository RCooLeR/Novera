package csv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"novera/internal/bigfile/fileio"
)

type csvOutputWriter func(context.Context, string, string) error

func safeNonSQLCSVOutputWriters() map[string]csvOutputWriter {
	return map[string]csvOutputWriter{
		"add-column": func(ctx context.Context, src, dst string) error {
			_, err := AddColumnFile(ctx, src, dst, AddColumnOptions{Delimiter: ',', Position: 1, Value: "added"})
			return err
		},
		"dedupe": func(ctx context.Context, src, dst string) error {
			_, err := DedupeRowsFile(ctx, src, dst, DedupeOptions{Delimiter: ',', HasHeader: true, KeyColumn: 0})
			return err
		},
		"filter": func(ctx context.Context, src, dst string) error {
			_, err := FilterRowsFile(ctx, src, dst, FilterOptions{Delimiter: ',', HasHeader: true, Column: 0, Op: "nonempty"})
			return err
		},
		"jsonl": func(ctx context.Context, src, dst string) error {
			_, err := ExportJSONLFile(ctx, src, dst, JSONLOptions{Delimiter: ',', HasHeader: true})
			return err
		},
		"project": func(ctx context.Context, src, dst string) error {
			_, err := ProjectColumnsFile(ctx, src, dst, ProjectOptions{Delimiter: ',', Columns: []int{0}})
			return err
		},
		"redact": func(ctx context.Context, src, dst string) error {
			_, err := RedactColumnsFile(ctx, src, dst, RedactOptions{
				Delimiter: ',', HasHeader: true, Columns: map[int]RedactMode{1: RedactFixed}, Replacement: "MASKED",
			})
			return err
		},
		"sample": func(ctx context.Context, src, dst string) error {
			_, err := SampleRowsFile(ctx, src, dst, SampleOptions{Delimiter: ',', HasHeader: true, EveryN: 2})
			return err
		},
	}
}

func TestNonSQLCSVOutputsRejectSourceAliases(t *testing.T) {
	for writerName, write := range safeNonSQLCSVOutputWriters() {
		t.Run(writerName, func(t *testing.T) {
			t.Run("same path", func(t *testing.T) {
				src := writeCSVOutputSafetySource(t, t.TempDir())
				if err := write(context.Background(), src, src); !errors.Is(err, fileio.ErrSourceAlias) {
					t.Fatalf("error = %v, want ErrSourceAlias", err)
				}
				assertCSVOutputSafetyContent(t, src, "id,value\n1,alpha\n2,beta\n")
			})

			t.Run("hard link", func(t *testing.T) {
				dir := t.TempDir()
				src := writeCSVOutputSafetySource(t, dir)
				dst := filepath.Join(dir, "hard-output")
				if err := os.Link(src, dst); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
				if err := write(context.Background(), src, dst); !errors.Is(err, fileio.ErrSourceAlias) {
					t.Fatalf("error = %v, want ErrSourceAlias", err)
				}
				assertCSVOutputSafetyContent(t, src, "id,value\n1,alpha\n2,beta\n")
			})
		})
	}
}

func TestNonSQLCSVOutputsPreserveExistingDestination(t *testing.T) {
	for writerName, write := range safeNonSQLCSVOutputWriters() {
		t.Run(writerName, func(t *testing.T) {
			dir := t.TempDir()
			src := writeCSVOutputSafetySource(t, dir)
			dst := filepath.Join(dir, "existing-output")
			if err := os.WriteFile(dst, []byte("sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := write(context.Background(), src, dst); !errors.Is(err, fileio.ErrExists) {
				t.Fatalf("error = %v, want ErrExists", err)
			}
			assertCSVOutputSafetyContent(t, dst, "sentinel")
			assertCSVOutputSafetyEntries(t, dir, 2)
		})
	}
}

func TestNonSQLCSVOutputsCancelWithoutPublishing(t *testing.T) {
	for writerName, write := range safeNonSQLCSVOutputWriters() {
		t.Run(writerName, func(t *testing.T) {
			dir := t.TempDir()
			src := writeCSVOutputSafetySource(t, dir)
			dst := filepath.Join(dir, "canceled-output")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := write(ctx, src, dst); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
			if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("canceled output exists: %v", err)
			}
			assertCSVOutputSafetyEntries(t, dir, 1)
		})
	}
}

func TestNonSQLCSVOutputsArePrivateAndLeaveOnlyFinal(t *testing.T) {
	for writerName, write := range safeNonSQLCSVOutputWriters() {
		t.Run(writerName, func(t *testing.T) {
			dir := t.TempDir()
			src := writeCSVOutputSafetySource(t, dir)
			dst := filepath.Join(dir, fmt.Sprintf("output-%s", writerName))
			if err := write(context.Background(), src, dst); err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(dst)
				if err != nil {
					t.Fatal(err)
				}
				if got := info.Mode().Perm(); got != 0o600 {
					t.Fatalf("mode = %04o, want 0600", got)
				}
			}
			assertCSVOutputSafetyEntries(t, dir, 2)
		})
	}
}

func writeCSVOutputSafetySource(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "input.csv")
	if err := os.WriteFile(path, []byte("id,value\n1,alpha\n2,beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertCSVOutputSafetyContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func assertCSVOutputSafetyEntries(t *testing.T, dir string, want int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != want {
		t.Fatalf("directory entries = %v, want %d", entries, want)
	}
}
