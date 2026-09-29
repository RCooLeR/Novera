package replace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/regularfile"
)

// LoadBatchRuleFile reads batch rule text from disk and validates that it
// contains at least one enabled rule.
func LoadBatchRuleFile(path string) (string, BatchRuleSet, error) {
	return LoadBatchRuleFileContext(context.Background(), path)
}

// LoadBatchRuleFileContext incrementally reads and parses a bounded regular
// UTF-8 rule file. It retains the exact text because callers edit it.
func LoadBatchRuleFileContext(ctx context.Context, path string) (string, BatchRuleSet, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", BatchRuleSet{}, err
	}
	file, err := regularfile.Open(path)
	if err != nil {
		return "", BatchRuleSet{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", BatchRuleSet{}, err
	}
	if !info.Mode().IsRegular() {
		return "", BatchRuleSet{}, fmt.Errorf("batch rule file must be regular: %s", path)
	}
	if info.Size() < 0 {
		return "", BatchRuleSet{}, errors.New("batch rule file size must not be negative")
	}
	if info.Size() > MaxBatchRuleFileBytes {
		return "", BatchRuleSet{}, &BatchRuleLimitError{Limit: "rule file bytes", Value: info.Size(), Max: MaxBatchRuleFileBytes}
	}
	var text strings.Builder
	text.Grow(int(info.Size()))
	limited := io.LimitReader(contextReader{ctx: ctx, r: file}, MaxBatchRuleFileBytes+1)
	set, err := parseBatchRuleReader(ctx, io.TeeReader(limited, &text))
	if err != nil {
		return "", BatchRuleSet{}, err
	}
	if text.Len() > MaxBatchRuleFileBytes {
		return "", BatchRuleSet{}, batchRuleLimit("rule file bytes", text.Len(), MaxBatchRuleFileBytes)
	}
	return text.String(), set, nil
}

// SaveBatchRuleFile validates batch rule text and writes it to disk as UTF-8.
func SaveBatchRuleFile(path string, text string) (BatchRuleSet, error) {
	if len(text) > MaxBatchRuleFileBytes {
		return BatchRuleSet{}, batchRuleLimit("rule text bytes", len(text), MaxBatchRuleFileBytes)
	}
	set, err := ParseBatchRuleSet(text)
	if err != nil {
		return BatchRuleSet{}, err
	}
	if _, err := fileio.WriteFileAtomic(path, []byte(text), fileio.AtomicWriteOptions{
		Mode:      0o600,
		Overwrite: true,
	}); err != nil {
		return BatchRuleSet{}, err
	}
	return set, nil
}
