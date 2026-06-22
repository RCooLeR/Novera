package replace

import (
	"os"

	"novera/internal/bigfile/fileio"
)

// LoadBatchRuleFile reads batch rule text from disk and validates that it
// contains at least one enabled rule.
func LoadBatchRuleFile(path string) (string, BatchRuleSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", BatchRuleSet{}, err
	}
	text := string(data)
	set, err := ParseBatchRuleSet(text)
	if err != nil {
		return "", BatchRuleSet{}, err
	}
	return text, set, nil
}

// SaveBatchRuleFile validates batch rule text and writes it to disk as UTF-8.
func SaveBatchRuleFile(path string, text string) (BatchRuleSet, error) {
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
