package extract

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/plugins/sql/analyze"
)

func TestSplitByTablePreviewAnnotatesHeaderlessSlices(t *testing.T) {
	summary := analyze.Summary{
		Tables:   []analyze.Table{{Name: "orders", CreateOffset: 0, InsertOffset: 40}},
		Charsets: map[string]int{"utf8mb4": 2, "latin1": 1},
	}
	preview, err := SplitByTablePreview(summary, 100, PlanOptions{OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if preview.HeaderIncluded {
		t.Fatal("slices begin at CREATE/INSERT, HeaderIncluded should be false")
	}
	if preview.Note == "" {
		t.Fatal("expected a note explaining slices omit the dump preamble")
	}
	if len(preview.DetectedCharsets) != 2 || preview.DetectedCharsets[0] != "latin1" || preview.DetectedCharsets[1] != "utf8mb4" {
		t.Fatalf("DetectedCharsets = %#v, want sorted [latin1 utf8mb4]", preview.DetectedCharsets)
	}
}

func TestPlanTableRangesUsesNextTableBoundaryAndSafeOutputs(t *testing.T) {
	summary := analyze.Summary{Tables: []analyze.Table{
		{Name: "orders", CreateOffset: 80, InsertOffset: 140},
		{Name: "app.Users", CreateOffset: 10, InsertOffset: 40},
		{Name: "unsafe/name", CreateOffset: 150, InsertOffset: -1},
	}}

	preview, err := SplitByTablePreview(summary, 220, PlanOptions{OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Operation != "sql-split-by-table" || preview.SourceSize != 220 {
		t.Fatalf("preview metadata = %#v", preview)
	}
	if len(preview.Tables) != 3 {
		t.Fatalf("tables = %d, want 3", len(preview.Tables))
	}
	assertRange(t, preview.Tables[0], "app.Users", 10, 80)
	assertRange(t, preview.Tables[1], "orders", 80, 150)
	assertRange(t, preview.Tables[2], "unsafe/name", 150, 220)
	if got := filepath.Base(preview.Tables[0].OutputPath); got != "app_Users.sql" {
		t.Fatalf("output 0 = %q", got)
	}
	if got := filepath.Base(preview.Tables[2].OutputPath); got != "unsafe_name.sql" {
		t.Fatalf("output 2 = %q", got)
	}
}

func TestPlanTableRangesFallsBackToInsertOffset(t *testing.T) {
	summary := analyze.Summary{Tables: []analyze.Table{
		{Name: "insert_only", CreateOffset: -1, InsertOffset: 25},
	}}

	ranges, err := PlanTableRanges(summary, 100, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 1 {
		t.Fatalf("ranges = %d, want 1", len(ranges))
	}
	assertRange(t, ranges[0], "insert_only", 25, 100)
	if ranges[0].OutputPath != "" {
		t.Fatalf("output path = %q, want empty without output dir", ranges[0].OutputPath)
	}
}

func TestPlanExtractTableIsCaseSensitive(t *testing.T) {
	summary := analyze.Summary{Tables: []analyze.Table{
		{Name: "Users", CreateOffset: 10, InsertOffset: -1},
		{Name: "users", CreateOffset: 50, InsertOffset: -1},
	}}

	preview, err := ExtractTablePreview(summary, 90, "users", PlanOptions{OutputDir: t.TempDir(), Extension: "dump"})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Operation != "sql-extract-table" || len(preview.Tables) != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	assertRange(t, preview.Tables[0], "users", 50, 90)
	if got := filepath.Base(preview.Tables[0].OutputPath); got != "users_02.dump" {
		t.Fatalf("output path = %q", got)
	}
}

func TestPlanExtractTableMissing(t *testing.T) {
	summary := analyze.Summary{Tables: []analyze.Table{{Name: "users", CreateOffset: 10, InsertOffset: -1}}}
	if _, err := PlanExtractTable(summary, 50, "Users", PlanOptions{}); err == nil {
		t.Fatal("expected case-sensitive missing table error")
	}
}

func TestPlanExtractTableBoundsQualifiedDisplayIdentity(t *testing.T) {
	component := strings.Repeat("`", analyze.MaxIdentifierBytes)
	display := "`" + strings.ReplaceAll(component, "`", "``") + "`.`" + strings.ReplaceAll(component, "`", "``") + "`"
	if len(display) != MaxTableSelectionBytes {
		t.Fatalf("qualified display length = %d, want %d", len(display), MaxTableSelectionBytes)
	}
	summary := analyze.Summary{Tables: []analyze.Table{{Name: display, CreateOffset: 0, InsertOffset: -1}}}
	if _, err := PlanExtractTable(summary, 10, display, PlanOptions{}); err != nil {
		t.Fatalf("largest analyzer identity rejected: %v", err)
	}
	if _, err := PlanExtractTable(summary, 10, display+"x", PlanOptions{}); !errors.Is(err, ErrTableNameTooLong) {
		t.Fatalf("oversized display error = %v, want ErrTableNameTooLong", err)
	}
}

func TestPlanTableRangesRejectsInvalidInputs(t *testing.T) {
	if _, err := PlanTableRanges(analyze.Summary{}, -1, PlanOptions{}); err == nil {
		t.Fatal("expected negative source size error")
	}
	if _, err := PlanTableRanges(analyze.Summary{}, 10, PlanOptions{}); err == nil {
		t.Fatal("expected no tables error")
	}
}

func TestPlanTableRangesAvoidsDuplicateOutputNames(t *testing.T) {
	summary := analyze.Summary{Tables: []analyze.Table{
		{Name: "a.b", CreateOffset: 0, InsertOffset: -1},
		{Name: "a/b", CreateOffset: 10, InsertOffset: -1},
	}}

	ranges, err := PlanTableRanges(summary, 20, PlanOptions{OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(ranges[0].OutputPath); got != "a_b.sql" {
		t.Fatalf("first output = %q", got)
	}
	if got := filepath.Base(ranges[1].OutputPath); got != "a_b_02.sql" {
		t.Fatalf("second output = %q", got)
	}
}

func TestPlanTableRangesRejectsUnsafeExtensionsBeforePathConstruction(t *testing.T) {
	summary := analyze.Summary{Tables: []analyze.Table{{Name: "users", CreateOffset: 0, InsertOffset: -1}}}
	for _, extension := range []string{
		"../escape", `..\escape`, "/absolute", `C:\escape`, "two.parts", ".", " sql", "sql ", "sql\x00x", strings.Repeat("a", maxExtractExtensionBytes),
	} {
		t.Run(strings.ReplaceAll(extension, "\\", "_"), func(t *testing.T) {
			if _, err := PlanTableRanges(summary, 10, PlanOptions{OutputDir: t.TempDir(), Extension: extension}); err == nil {
				t.Fatalf("extension %q unexpectedly accepted", extension)
			}
		})
	}
}

func TestPlanTableRangesUsesPortableCollisionKeysAndReservedNames(t *testing.T) {
	outDir := t.TempDir()
	summary := analyze.Summary{Tables: []analyze.Table{
		{Name: "Users", CreateOffset: 0, InsertOffset: -1},
		{Name: "users", CreateOffset: 10, InsertOffset: -1},
		{Name: "e\u0301", CreateOffset: 20, InsertOffset: -1},
		{Name: "\u00e9", CreateOffset: 30, InsertOffset: -1},
		{Name: "CON", CreateOffset: 40, InsertOffset: -1},
		{Name: "lpt1...   ", CreateOffset: 50, InsertOffset: -1},
	}}
	ranges, err := PlanTableRanges(summary, 60, PlanOptions{OutputDir: outDir})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Users.sql", "users_02.sql", "\u00e9.sql", "\u00e9_02.sql", "_CON.sql", "_lpt1.sql"}
	for i, table := range ranges {
		if got := filepath.Base(table.OutputPath); got != want[i] {
			t.Fatalf("output %d = %q, want %q", i, got, want[i])
		}
		if err := requireDirectChild(outDir, table.OutputPath); err != nil {
			t.Fatalf("output %q escaped selected directory: %v", table.OutputPath, err)
		}
	}
}

func TestSafeFilenameComponentBoundsLongUnicodeNames(t *testing.T) {
	name := strings.Repeat("\u754c", 300)
	first := SafeFilenameComponent(name)
	second := SafeFilenameComponent(name)
	if first != second {
		t.Fatalf("component is not deterministic: %q != %q", first, second)
	}
	if len(first) > maxExtractBaseBytes {
		t.Fatalf("component bytes = %d, want <= %d", len(first), maxExtractBaseBytes)
	}
	if !strings.Contains(first, "_") {
		t.Fatalf("truncated component lacks stable hash suffix: %q", first)
	}
	if filepath.Base(first) != first || first == "." || first == ".." {
		t.Fatalf("unsafe component %q", first)
	}
}

func assertRange(t *testing.T, got TableRange, name string, start int64, end int64) {
	t.Helper()
	if got.Name != name || got.StartOffset != start || got.EndOffset != end || got.Bytes != end-start {
		t.Fatalf("range = %+v, want %q [%d,%d)", got, name, start, end)
	}
}
