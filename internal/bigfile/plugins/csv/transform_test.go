package csv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o666); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func readAll(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestFilterRowsFile(t *testing.T) {
	src := writeTemp(t, "in.csv", "id,name,age\n1,alice,30\n2,bob,17\n3,carol,40\n")
	dst := filepath.Join(t.TempDir(), "out.csv")
	sum, err := FilterRowsFile(context.Background(), src, dst, FilterOptions{
		Delimiter: ',', HasHeader: true, Column: 2, Op: "gt", Value: "18",
	})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if sum.RecordsRead != 4 {
		t.Fatalf("read = %d, want 4", sum.RecordsRead)
	}
	got := readAll(t, dst)
	if !strings.Contains(got, "alice") || !strings.Contains(got, "carol") {
		t.Fatalf("missing kept rows: %q", got)
	}
	if strings.Contains(got, "bob") {
		t.Fatalf("bob should be filtered out: %q", got)
	}
	// header + 2 data rows kept
	if sum.RecordsWritten != 3 {
		t.Fatalf("written = %d, want 3", sum.RecordsWritten)
	}
}

func TestFilterRowsNegate(t *testing.T) {
	src := writeTemp(t, "in.csv", "id,status\n1,active\n2,banned\n3,active\n")
	dst := filepath.Join(t.TempDir(), "out.csv")
	_, err := FilterRowsFile(context.Background(), src, dst, FilterOptions{
		Delimiter: ',', HasHeader: true, Column: 1, Op: "eq", Value: "banned", Negate: true,
	})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	got := readAll(t, dst)
	if strings.Contains(got, "banned") {
		t.Fatalf("banned should be excluded: %q", got)
	}
	if !strings.Contains(got, "active") {
		t.Fatalf("active rows should remain: %q", got)
	}
}

func TestDedupeRowsByKey(t *testing.T) {
	src := writeTemp(t, "in.csv", "id,email\n1,a@x.com\n2,b@x.com\n3,a@x.com\n4,c@x.com\n")
	dst := filepath.Join(t.TempDir(), "out.csv")
	sum, err := DedupeRowsFile(context.Background(), src, dst, DedupeOptions{
		Delimiter: ',', HasHeader: true, KeyColumn: 1,
	})
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	// header + 3 distinct emails
	if sum.RecordsWritten != 4 {
		t.Fatalf("written = %d, want 4", sum.RecordsWritten)
	}
	got := readAll(t, dst)
	if strings.Count(got, "a@x.com") != 1 {
		t.Fatalf("a@x.com should appear once: %q", got)
	}
}

func TestDedupeRowsWholeRow(t *testing.T) {
	src := writeTemp(t, "in.csv", "a,b\n1,2\n1,2\n3,4\n")
	dst := filepath.Join(t.TempDir(), "out.csv")
	sum, err := DedupeRowsFile(context.Background(), src, dst, DedupeOptions{
		Delimiter: ',', HasHeader: true, KeyColumn: -1,
	})
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	if sum.RecordsWritten != 3 { // header + (1,2) + (3,4)
		t.Fatalf("written = %d, want 3", sum.RecordsWritten)
	}
}

func TestDedupeRaggedRowsNotCollapsed(t *testing.T) {
	// Rows shorter than the key column must NOT all collapse to one "duplicate",
	// and an empty key cell must be distinct from a missing key cell.
	src := writeTemp(t, "in.csv", "id,email\n1,a@x.com\nshort1\nshort2\n4,\n5,\n")
	dst := filepath.Join(t.TempDir(), "out.csv")
	sum, err := DedupeRowsFile(context.Background(), src, dst, DedupeOptions{
		Delimiter: ',', HasHeader: true, KeyColumn: 1,
	})
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	got := readAll(t, dst)
	// header + a@x.com + short1 + short2 + first empty-key row (4,) — the second
	// empty-key row (5,) is a real duplicate key "" so it is dropped.
	if !strings.Contains(got, "short1") || !strings.Contains(got, "short2") {
		t.Fatalf("distinct short rows were collapsed: %q", got)
	}
	if sum.RecordsWritten != 5 {
		t.Fatalf("written = %d, want 5\n%s", sum.RecordsWritten, got)
	}
}

func TestDedupeRowsWithEmbeddedNULDoNotCollide(t *testing.T) {
	tests := []struct {
		name      string
		keyColumn int
	}{
		{name: "whole row", keyColumn: -1},
		{name: "missing key column", keyColumn: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// These two rows have the same NUL-joined spelling but different
			// field boundaries. Both must survive deduplication.
			input := "a\x00b,c\na,b\x00c\n"
			src := writeTemp(t, "in.csv", input)
			dst := filepath.Join(t.TempDir(), "out.csv")

			sum, err := DedupeRowsFile(context.Background(), src, dst, DedupeOptions{
				Delimiter: ',', KeyColumn: test.keyColumn,
			})
			if err != nil {
				t.Fatalf("DedupeRowsFile() error = %v", err)
			}
			if sum.RecordsWritten != 2 {
				t.Fatalf("records written = %d, want 2", sum.RecordsWritten)
			}
			if got := readAll(t, dst); got != input {
				t.Fatalf("output = %q, want both distinct rows %q", got, input)
			}
		})
	}
}

func TestSampleRowsFile(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("n\n")
	for i := 0; i < 100; i++ {
		sb.WriteString(itoa(i))
		sb.WriteString("\n")
	}
	src := writeTemp(t, "in.csv", sb.String())
	dst := filepath.Join(t.TempDir(), "out.csv")
	sum, err := SampleRowsFile(context.Background(), src, dst, SampleOptions{
		Delimiter: ',', HasHeader: true, EveryN: 10,
	})
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	// header + rows 0,10,20,...,90 = 1 + 10
	if sum.RecordsWritten != 11 {
		t.Fatalf("written = %d, want 11", sum.RecordsWritten)
	}
}

func TestTransformRejectsSamePath(t *testing.T) {
	src := writeTemp(t, "in.csv", "a\n1\n")
	_, err := FilterRowsFile(context.Background(), src, src, FilterOptions{Op: "nonempty", Column: 0})
	if err == nil {
		t.Fatal("expected error for same input/output path")
	}
}

func TestStreamingTransformsDoNotClobberPredictableScratchPath(t *testing.T) {
	tests := []struct {
		name string
		run  func(src, dst string) error
	}{
		{
			name: "row transform",
			run: func(src, dst string) error {
				_, err := FilterRowsFile(context.Background(), src, dst, FilterOptions{
					Column: 0, Op: "nonempty",
				})
				return err
			},
		},
		{
			name: "JSONL export",
			run: func(src, dst string) error {
				_, err := ExportJSONLFile(context.Background(), src, dst, JSONLOptions{})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "in.csv")
			dst := filepath.Join(dir, "out")
			if err := os.WriteFile(src, []byte("value\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			legacyScratch := tempOutputPath(dst)
			const sentinel = "unowned scratch"
			if err := os.WriteFile(legacyScratch, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := test.run(src, dst); err != nil {
				t.Fatal(err)
			}
			if got := readAll(t, legacyScratch); got != sentinel {
				t.Fatalf("predictable scratch changed to %q", got)
			}
			pattern := filepath.Join(dir, "."+filepath.Base(dst)+".quarry-part-*")
			if matches, err := filepath.Glob(pattern); err != nil {
				t.Fatal(err)
			} else if len(matches) != 0 {
				t.Fatalf("owned scratch files remain after publish: %v", matches)
			}
		})
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
