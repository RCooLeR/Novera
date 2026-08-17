package replace

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

func parseBatchRuleReader(ctx context.Context, source io.Reader) (BatchRuleSet, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reader := bufio.NewReaderSize(source, maxBatchRuleLineBytes+1)
	rules := make([]BatchRule, 0, 64)
	disabled := 0
	lineNumber := 0
	totalBytes := 0
	budget := batchRuleBudget{}

	for {
		if err := ctx.Err(); err != nil {
			return BatchRuleSet{}, err
		}
		raw, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			return BatchRuleSet{}, batchRuleLimit("rule line bytes", len(raw), maxBatchRuleLineBytes)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return BatchRuleSet{}, readErr
		}
		if len(raw) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		lineNumber++
		totalBytes += len(raw)
		if totalBytes > MaxBatchRuleFileBytes {
			return BatchRuleSet{}, batchRuleLimit("rule text bytes", totalBytes, MaxBatchRuleFileBytes)
		}
		if !utf8.Valid(raw) {
			return BatchRuleSet{}, fmt.Errorf("line %d: batch rules must be valid UTF-8", lineNumber)
		}

		line := strings.TrimSpace(string(raw))
		if line != "" && !strings.HasPrefix(line, "#") {
			disabledRule := false
			if strings.HasPrefix(line, "!") {
				disabledRule = true
				line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
				if line == "" || strings.HasPrefix(line, "#") {
					disabled++
					if budget.statements == MaxBatchRules {
						return BatchRuleSet{}, batchRuleLimit("rule count", budget.statements+1, MaxBatchRules)
					}
					budget.statements++
					if errors.Is(readErr, io.EOF) {
						break
					}
					continue
				}
			}

			separator := "=>"
			separatorAt := strings.Index(line, separator)
			if separatorAt < 0 {
				separator = "->"
				separatorAt = strings.Index(line, separator)
			}
			if separatorAt < 0 {
				return BatchRuleSet{}, fmt.Errorf("line %d: expected `find => replace`", lineNumber)
			}
			find := strings.TrimSpace(line[:separatorAt])
			replacement := strings.TrimSpace(line[separatorAt+len(separator):])
			name := ""
			if nameAt := strings.Index(find, "::"); nameAt >= 0 {
				name = strings.TrimSpace(find[:nameAt])
				find = strings.TrimSpace(find[nameAt+2:])
			}
			if find == "" {
				return BatchRuleSet{}, fmt.Errorf("line %d: empty search text", lineNumber)
			}
			if name == "" {
				name = fmt.Sprintf("Rule %d", len(rules)+1)
			}
			if err := budget.add(name, find, replacement); err != nil {
				return BatchRuleSet{}, fmt.Errorf("line %d: %w", lineNumber, err)
			}
			if disabledRule {
				disabled++
			} else {
				rules = append(rules, BatchRule{
					Name:     name,
					Find:     []byte(find),
					Replace:  []byte(replacement),
					Priority: len(rules),
				})
			}
		}

		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	if len(rules) == 0 {
		if disabled > 0 {
			return BatchRuleSet{}, errors.New("no enabled batch rules")
		}
		return BatchRuleSet{}, errors.New("no batch rules")
	}
	return BatchRuleSet{Rules: rules, DisabledRules: disabled}, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
