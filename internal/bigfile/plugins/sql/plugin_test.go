package sql

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memReader struct {
	data []byte
}

func (m memReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m memReader) Size() int64 {
	return int64(len(m.data))
}

func TestSQLPluginRoutesAnalyzer(t *testing.T) {
	summary, err := Analyze(context.Background(), memReader{data: []byte("CREATE TABLE `users` (id int);\nINSERT INTO `users` VALUES (1);")}, AnalyzeOptions{ChunkSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 {
		t.Fatalf("tables = %d, want 1", len(summary.Tables))
	}
	if summary.Tables[0].Name != "users" {
		t.Fatalf("table = %q, want users", summary.Tables[0].Name)
	}
	if summary.Tables[0].CreateOffset < 0 || summary.Tables[0].InsertOffset < 0 {
		t.Fatalf("expected create and insert offsets, got %+v", summary.Tables[0])
	}
}

func TestSQLRuntimePluginRoutesAnalyzer(t *testing.T) {
	var runtime Runtime = RuntimePlugin()
	if runtime.Descriptor().ID != "sql" {
		t.Fatalf("descriptor id = %q, want sql", runtime.Descriptor().ID)
	}
	summary, err := runtime.Analyze(context.Background(), memReader{data: []byte("CREATE TABLE `users` (id int);\n")}, AnalyzeOptions{ChunkSize: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != "users" {
		t.Fatalf("tables = %#v", summary.Tables)
	}
}

func TestSQLPluginRoutesPresetBuilder(t *testing.T) {
	cfg, err := BuildPreset(ChangeDatabasePreset, "old_db", "new_db", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != PresetModeBatch {
		t.Fatalf("mode = %q, want %q", cfg.Mode, PresetModeBatch)
	}
	rules := BatchRules(cfg)
	if len(rules) != 3 {
		t.Fatalf("rules = %d, want 3", len(rules))
	}
	if string(rules[0].Find) != "`old_db`" || string(rules[0].Replace) != "`new_db`" {
		t.Fatalf("first rule = %q => %q", rules[0].Find, rules[0].Replace)
	}
}

func TestSQLRuntimePluginRoutesPresetBuilder(t *testing.T) {
	var runtime Runtime = RuntimePlugin()
	cfg, err := runtime.BuildPreset(ChangeDatabasePreset, "old_db", "new_db", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rules := runtime.BatchRules(cfg); len(rules) != 3 {
		t.Fatalf("rules = %d, want 3", len(rules))
	}
}

func TestSQLRuntimePluginRoutesExtractPreviews(t *testing.T) {
	var runtime Runtime = RuntimePlugin()
	preview, err := runtime.ExtractTablePreview(Summary{
		Tables: []Table{
			{Name: "users", CreateOffset: 12, InsertOffset: 40},
			{Name: "orders", CreateOffset: 90, InsertOffset: -1},
		},
	}, 140, "users", ExtractPlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Operation != "sql-extract-table" || len(preview.Tables) != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	if got := preview.Tables[0]; got.Name != "users" || got.StartOffset != 12 || got.EndOffset != 90 {
		t.Fatalf("range = %+v", got)
	}
}

func TestSQLRuntimePluginRoutesExtractWrites(t *testing.T) {
	var runtime Runtime = RuntimePlugin()
	src := "CREATE TABLE users(id int);\nCREATE TABLE orders(id int);\n"
	outDir := filepath.Join(t.TempDir(), "out")
	summary, err := runtime.ExtractTable(context.Background(), memReader{data: []byte(src)}, "dump.sql", Summary{
		Tables: []Table{
			{Name: "users", CreateOffset: 0, InsertOffset: -1},
			{Name: "orders", CreateOffset: int64(strings.Index(src, "CREATE TABLE orders")), InsertOffset: -1},
		},
	}, "orders", ExtractWriteOptions{PlanOptions: ExtractPlanOptions{OutputDir: outDir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Outputs) != 1 || summary.Outputs[0].Name != "orders" {
		t.Fatalf("summary = %#v", summary)
	}
	if got, err := os.ReadFile(summary.Outputs[0].OutputPath); err != nil {
		t.Fatal(err)
	} else if string(got) != "CREATE TABLE orders(id int);\n" {
		t.Fatalf("output = %q", string(got))
	}
}

func TestSQLPluginRoutesVisibleHighlighting(t *testing.T) {
	tokens := HighlightVisible("SELECT * FROM `users` WHERE id = 42;")
	if len(tokens) == 0 {
		t.Fatal("expected tokens")
	}
	var keyword, identifier, number bool
	for _, tok := range tokens {
		keyword = keyword || tok.Kind == TokenKeyword
		identifier = identifier || tok.Kind == TokenIdentifier
		number = number || tok.Kind == TokenNumber
	}
	if !keyword || !identifier || !number {
		t.Fatalf("expected keyword, identifier, and number tokens, got %+v", tokens)
	}
}

func TestSQLRuntimePluginRoutesVisibleHighlighting(t *testing.T) {
	var runtime Runtime = RuntimePlugin()
	tokens := runtime.HighlightVisible("SELECT 42;")
	if len(tokens) == 0 {
		t.Fatal("expected tokens")
	}
}
