package bigfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlanalyze "novera/internal/bigfile/plugins/sql/analyze"
)

func TestSQLAnalyzeFailureReturnsNoPartialResultOrCache(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "routine.sql")
	source := "CREATE TABLE before_routine (id int);\n" +
		"DELIMITER $$\n" +
		"CREATE PROCEDURE p()\nBEGIN\n" +
		"  INSERT INTO phantom VALUES (1);\nEND$$\n" +
		"DELIMITER ;\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })

	result, err := service.SqlAnalyze(meta.FileID)
	if !errors.Is(err, sqlanalyze.ErrUnsupportedDelimiter) {
		t.Fatalf("SqlAnalyze error = %v, want ErrUnsupportedDelimiter", err)
	}
	if len(result.Tables) != 0 || result.CreateTables != 0 || result.InsertTables != 0 || result.DefinerCount != 0 || result.Header {
		t.Fatalf("failed analysis exposed a partial result: %#v", result)
	}
	service.sqlMu.Lock()
	_, cached := service.sqlSummary[meta.FileID]
	service.sqlMu.Unlock()
	if cached {
		t.Fatal("failed analysis populated the SQL summary cache")
	}

	dialogCalls := 0
	previousDialog := sqlSaveDialog
	sqlSaveDialog = func(string, string) (string, error) {
		dialogCalls++
		return filepath.Join(dir, "must-not-exist.sql"), nil
	}
	t.Cleanup(func() { sqlSaveDialog = previousDialog })
	if _, err := service.SqlExtractTableViaDialog(meta.FileID, "phantom"); !errors.Is(err, ErrSQLAnalysisRequired) {
		t.Fatalf("extract after failed analysis error = %v, want ErrSQLAnalysisRequired", err)
	}
	if dialogCalls != 0 {
		t.Fatalf("failed analysis consumer opened %d dialog(s)", dialogCalls)
	}
}

func TestSQLServiceExactRegionsExcludeUnrelatedDML(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "regions.sql")
	source := "SET NAMES utf8mb4;\n" +
		"CREATE TABLE `records` (`id` int);\n" +
		"UPDATE `records` SET `id` = 99;\n" +
		"INSERT INTO `records` VALUES (1);\n" +
		"DELETE FROM `records` WHERE `id` = 1;\n" +
		"REPLACE INTO `records` VALUES (2);\n" +
		"ALTER TABLE `records` ADD INDEX (`id`);\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	if _, err := service.SqlAnalyze(meta.FileID); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dir, "table.sql")
	previousDialog := sqlSaveDialog
	sqlSaveDialog = func(string, string) (string, error) { return outputPath, nil }
	t.Cleanup(func() { sqlSaveDialog = previousDialog })

	if _, err := service.SqlExtractTableViaDialog(meta.FileID, "records"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE TABLE `records` (`id` int);" +
		"INSERT INTO `records` VALUES (1);" +
		"REPLACE INTO `records` VALUES (2);"
	if string(got) != want {
		t.Fatalf("exact-region table output = %q, want %q", got, want)
	}
	for _, excluded := range []string{"SET NAMES", "UPDATE", "DELETE", "ALTER TABLE"} {
		if strings.Contains(string(got), excluded) {
			t.Fatalf("exact-region output retained unrelated %s statement", excluded)
		}
	}
}

func TestSQLSummaryCacheEvictsLeastRecentlyUsedEntries(t *testing.T) {
	service := NewFileService()
	metas := make([]FileMeta, 0, maxCachedSQLSummaries+1)
	for index := 0; index < maxCachedSQLSummaries+1; index++ {
		path := filepath.Join(t.TempDir(), "cache.sql")
		source := []byte("CREATE TABLE t" + string(rune('a'+index)) + " (id int);\n")
		if err := os.WriteFile(path, source, 0o600); err != nil {
			t.Fatal(err)
		}
		meta, err := service.OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		metas = append(metas, meta)
		t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
		if _, err := service.SqlAnalyze(meta.FileID); err != nil {
			t.Fatal(err)
		}
	}

	service.sqlMu.Lock()
	cacheCount := len(service.sqlSummary)
	_, oldestPresent := service.sqlSummary[metas[0].FileID]
	_, newestPresent := service.sqlSummary[metas[len(metas)-1].FileID]
	service.sqlMu.Unlock()
	if cacheCount != maxCachedSQLSummaries {
		t.Fatalf("cached summaries = %d, want %d", cacheCount, maxCachedSQLSummaries)
	}
	if oldestPresent || !newestPresent {
		t.Fatalf("LRU cache ownership oldest=%v newest=%v", oldestPresent, newestPresent)
	}
	if _, _, err := service.sqlSummaryFor(metas[0].FileID); !errors.Is(err, ErrSQLAnalysisRequired) {
		t.Fatalf("evicted summary lookup error = %v, want ErrSQLAnalysisRequired", err)
	}
}
