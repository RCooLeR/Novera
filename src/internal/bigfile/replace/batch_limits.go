package replace

import (
	"errors"
	"fmt"
)

const (
	MaxBatchRuleFileBytes         = 8 * 1024 * 1024
	MaxBatchRules                 = 4096
	MaxBatchRuleNameBytes         = 256
	MaxBatchPatternBytes          = 64 * 1024
	MaxBatchReplacementBytes      = 256 * 1024
	MaxBatchPatternAggregateBytes = 1024 * 1024
	MaxBatchRuleAggregateBytes    = 8 * 1024 * 1024
	MaxBatchWriteBufferBytes      = 4 * 1024 * 1024
	defaultBatchWriteBufferBytes  = 1024 * 1024

	maxBatchRuleLineBytes = MaxBatchRuleNameBytes + MaxBatchPatternBytes + MaxBatchReplacementBytes + 32
)

var ErrBatchRuleLimit = errors.New("batch rule safety limit exceeded")

type BatchRuleLimitError struct {
	Limit string
	Value int64
	Max   int64
}

func (e *BatchRuleLimitError) Error() string {
	return fmt.Sprintf("%s: %s is %d, maximum is %d", ErrBatchRuleLimit, e.Limit, e.Value, e.Max)
}

func (e *BatchRuleLimitError) Unwrap() error { return ErrBatchRuleLimit }

func batchRuleLimit(limit string, value int, maxValue int) error {
	return &BatchRuleLimitError{Limit: limit, Value: int64(value), Max: int64(maxValue)}
}

type batchRuleBudget struct {
	statements int
	aggregate  int
	patterns   int
}

func (b *batchRuleBudget) add(name string, find string, replacement string) error {
	return b.addLengths(len(name), len(find), len(replacement))
}

func (b *batchRuleBudget) addLengths(nameBytes int, findBytes int, replacementBytes int) error {
	b.statements++
	if b.statements > MaxBatchRules {
		return batchRuleLimit("rule count", b.statements, MaxBatchRules)
	}
	if nameBytes > MaxBatchRuleNameBytes {
		return batchRuleLimit("rule name bytes", nameBytes, MaxBatchRuleNameBytes)
	}
	if findBytes == 0 {
		return errors.New("empty search text")
	}
	if findBytes > MaxBatchPatternBytes {
		return batchRuleLimit("pattern bytes", findBytes, MaxBatchPatternBytes)
	}
	if replacementBytes > MaxBatchReplacementBytes {
		return batchRuleLimit("replacement bytes", replacementBytes, MaxBatchReplacementBytes)
	}
	if findBytes > MaxBatchPatternAggregateBytes-b.patterns {
		return batchRuleLimit("aggregate pattern bytes", b.patterns+findBytes, MaxBatchPatternAggregateBytes)
	}
	b.patterns += findBytes
	added := nameBytes + findBytes + replacementBytes
	if added > MaxBatchRuleAggregateBytes-b.aggregate {
		return batchRuleLimit("aggregate rule bytes", b.aggregate+added, MaxBatchRuleAggregateBytes)
	}
	b.aggregate += added
	return nil
}

func validateBatchRules(rules []BatchRule) (maxPattern int, err error) {
	if len(rules) == 0 {
		return 0, errors.New("no batch rules")
	}
	if len(rules) > MaxBatchRules {
		return 0, batchRuleLimit("rule count", len(rules), MaxBatchRules)
	}
	budget := batchRuleBudget{}
	for i, rule := range rules {
		name := rule.Name
		if name == "" {
			name = fmt.Sprintf("Rule %d", i+1)
		}
		if err := budget.addLengths(len(name), len(rule.Find), len(rule.Replace)); err != nil {
			if err.Error() == "empty search text" {
				return 0, fmt.Errorf("rule %d has empty search text", i+1)
			}
			return 0, fmt.Errorf("rule %d: %w", i+1, err)
		}
		if len(rule.Find) > maxPattern {
			maxPattern = len(rule.Find)
		}
	}
	return maxPattern, nil
}

func batchWriteBufferSize(requested int) (int, error) {
	if requested < 0 {
		return 0, errors.New("batch write buffer size must not be negative")
	}
	if requested == 0 {
		return defaultBatchWriteBufferBytes, nil
	}
	if requested > MaxBatchWriteBufferBytes {
		return 0, batchRuleLimit("write buffer bytes", requested, MaxBatchWriteBufferBytes)
	}
	return requested, nil
}
