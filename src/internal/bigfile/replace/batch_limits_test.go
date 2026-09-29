package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func requireBatchLimit(t *testing.T, err error, limit string) {
	t.Helper()
	if !errors.Is(err, ErrBatchRuleLimit) {
		t.Fatalf("error = %v, want ErrBatchRuleLimit", err)
	}
	var detail *BatchRuleLimitError
	if !errors.As(err, &detail) {
		t.Fatalf("error = %T, want *BatchRuleLimitError", err)
	}
	if detail.Limit != limit {
		t.Fatalf("limit = %q, want %q", detail.Limit, limit)
	}
}

func TestParseBatchRuleSetEnforcesResourceAndEncodingLimits(t *testing.T) {
	t.Run("text bytes", func(t *testing.T) {
		_, err := ParseBatchRuleSet(strings.Repeat("#", MaxBatchRuleFileBytes+1))
		requireBatchLimit(t, err, "rule text bytes")
	})

	t.Run("rule count", func(t *testing.T) {
		var text strings.Builder
		for i := 0; i <= MaxBatchRules; i++ {
			text.WriteString("a => b\n")
		}
		_, err := ParseBatchRuleSet(text.String())
		requireBatchLimit(t, err, "rule count")
	})

	t.Run("name bytes", func(t *testing.T) {
		_, err := ParseBatchRuleSet(strings.Repeat("n", MaxBatchRuleNameBytes+1) + " :: a => b")
		requireBatchLimit(t, err, "rule name bytes")
	})

	t.Run("pattern bytes", func(t *testing.T) {
		_, err := ParseBatchRuleSet(strings.Repeat("a", MaxBatchPatternBytes+1) + " => b")
		requireBatchLimit(t, err, "pattern bytes")
	})

	t.Run("replacement bytes", func(t *testing.T) {
		_, err := ParseBatchRuleSet("a => " + strings.Repeat("b", MaxBatchReplacementBytes+1))
		requireBatchLimit(t, err, "replacement bytes")
	})

	t.Run("valid UTF-8", func(t *testing.T) {
		_, err := ParseBatchRuleSet(string([]byte{'a', ' ', '=', '>', ' ', 0xff}))
		if err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
			t.Fatalf("error = %v, want UTF-8 rejection", err)
		}
	})
}

func TestCompileBatchRulesEnforcesAggregateLimits(t *testing.T) {
	patterns := make([]BatchRule, MaxBatchPatternAggregateBytes/MaxBatchPatternBytes+1)
	for i := range patterns {
		patterns[i] = BatchRule{Find: []byte(strings.Repeat("a", MaxBatchPatternBytes))}
	}
	_, _, err := compileBatchRules(patterns, false)
	requireBatchLimit(t, err, "aggregate pattern bytes")

	replacements := make([]BatchRule, MaxBatchRuleAggregateBytes/MaxBatchReplacementBytes+1)
	for i := range replacements {
		replacements[i] = BatchRule{Find: []byte("a"), Replace: []byte(strings.Repeat("b", MaxBatchReplacementBytes))}
	}
	_, _, err = compileBatchRules(replacements, false)
	requireBatchLimit(t, err, "aggregate rule bytes")
}

func TestLoadBatchRuleFileRejectsOversizedSparseFileAndCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oversized.qrules")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxBatchRuleFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = LoadBatchRuleFile(path)
	requireBatchLimit(t, err, "rule file bytes")

	smallPath := filepath.Join(dir, "small.qrules")
	if err := os.WriteFile(smallPath, []byte("a => b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = LoadBatchRuleFileContext(ctx, smallPath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestSaveBatchRuleFileLimitPreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.qrules")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SaveBatchRuleFile(path, strings.Repeat("x", MaxBatchRuleFileBytes+1))
	requireBatchLimit(t, err, "rule text bytes")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "original" {
		t.Fatalf("file changed to %q", data)
	}
}

func TestBatchWriteBufferSizeIsBounded(t *testing.T) {
	if _, err := batchWriteBufferSize(-1); err == nil {
		t.Fatal("negative buffer size accepted")
	}
	_, err := batchWriteBufferSize(MaxBatchWriteBufferBytes + 1)
	requireBatchLimit(t, err, "write buffer bytes")
}
