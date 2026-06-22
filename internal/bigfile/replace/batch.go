package replace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"novera/internal/bigfile/asciifold"
)

// BatchRule is one plain-text batch replacement rule. When matches overlap,
// Quarry chooses the earliest match; ties use lower Priority, then longer match,
// then original rule order. Non-winning overlapping matches are counted as
// conflicts for preview/result reporting.
type BatchRule struct {
	Name     string
	Find     []byte
	Replace  []byte
	Priority int
}

// BatchOptions controls plain batch replacement.
type BatchOptions struct {
	ChunkSize       int
	CaseInsensitive bool
	WholeWord       bool
	Progress        func(Progress)
}

// BatchPreview describes one batch replacement preview row.
type BatchPreview struct {
	Offset    int64
	RuleName  string
	Before    string
	After     string
	Conflicts int
}

// BatchRuleSet describes parsed batch rules plus skipped disabled lines.
type BatchRuleSet struct {
	Rules         []BatchRule
	DisabledRules int
}

type compiledBatchRule struct {
	BatchRule
	order  int
	needle []byte
}

type batchCandidate struct {
	start int
	end   int
	rule  compiledBatchRule
}

// ParseBatchRuleSet parses batch rules from line-oriented text.
// Each non-empty, non-comment line must use either `=>` or `->`.
// Optional syntax:
//   - leading `!` disables a rule without deleting it
//   - `Name :: find => replace` assigns a custom rule name
func ParseBatchRuleSet(text string) (BatchRuleSet, error) {
	lines := strings.Split(text, "\n")
	rules := make([]BatchRule, 0, len(lines))
	disabled := 0
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		disabledRule := false
		if strings.HasPrefix(line, "!") {
			disabledRule = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
			if line == "" || strings.HasPrefix(line, "#") {
				disabled++
				continue
			}
		}
		sep := "=>"
		idx := strings.Index(line, sep)
		if idx < 0 {
			sep = "->"
			idx = strings.Index(line, sep)
		}
		if idx < 0 {
			return BatchRuleSet{}, fmt.Errorf("line %d: expected `find => replace`", i+1)
		}
		find := strings.TrimSpace(line[:idx])
		repl := strings.TrimSpace(line[idx+len(sep):])
		name := ""
		if nameIdx := strings.Index(find, "::"); nameIdx >= 0 {
			name = strings.TrimSpace(find[:nameIdx])
			find = strings.TrimSpace(find[nameIdx+2:])
		}
		if find == "" {
			return BatchRuleSet{}, fmt.Errorf("line %d: empty search text", i+1)
		}
		if disabledRule {
			disabled++
			continue
		}
		if name == "" {
			name = fmt.Sprintf("Rule %d", len(rules)+1)
		}
		rules = append(rules, BatchRule{
			Name:     name,
			Find:     []byte(find),
			Replace:  []byte(repl),
			Priority: len(rules),
		})
	}
	if len(rules) == 0 {
		if disabled > 0 {
			return BatchRuleSet{}, errors.New("no enabled batch rules")
		}
		return BatchRuleSet{}, errors.New("no batch rules")
	}
	return BatchRuleSet{Rules: rules, DisabledRules: disabled}, nil
}

// ParseBatchPlainRules parses plain batch rules from line-oriented text.
func ParseBatchPlainRules(text string) ([]BatchRule, error) {
	set, err := ParseBatchRuleSet(text)
	if err != nil {
		return nil, err
	}
	return set.Rules, nil
}

// ReplaceBatchPlain streams src to dst while applying plain-text rules in one pass.
func ReplaceBatchPlain(ctx context.Context, src *os.File, dst syncWriter, rules []BatchRule, opts BatchOptions) (int64, int64, error) {
	compiled, maxPattern, err := compileBatchRules(rules, opts.CaseInsensitive)
	if err != nil {
		return 0, 0, err
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 64 * 1024 * 1024
	}
	if opts.WholeWord && opts.ChunkSize < maxPattern+2 {
		opts.ChunkSize = maxPattern + 2
	}

	st, err := src.Stat()
	if err != nil {
		return 0, 0, err
	}

	total := st.Size()
	buf := make([]byte, opts.ChunkSize)
	keepSize := maxPattern - 1
	if opts.WholeWord {
		keepSize = maxPattern + 2
	}
	carry := make([]byte, 0, keepSize)
	window := make([]byte, 0, opts.ChunkSize+keepSize)

	var processed int64
	var matches int64
	var conflicts int64

	for {
		select {
		case <-ctx.Done():
			return matches, conflicts, ctx.Err()
		default:
		}

		n, readErr := src.Read(buf)
		if n > 0 {
			window = window[:0]
			window = append(window, carry...)
			window = append(window, buf[:n]...)
			processLimit := len(window) - keepSize
			if errors.Is(readErr, io.EOF) {
				processLimit = len(window)
			}
			if processLimit < 0 {
				processLimit = 0
			}

			consumed, count, conflictCount, err := writeBatchPrefix(dst, window, processLimit, total, processed-int64(len(carry)), compiled, opts.CaseInsensitive, opts.WholeWord)
			if err != nil {
				return matches, conflicts, err
			}
			matches += int64(count)
			conflicts += int64(conflictCount)

			carry = append(carry[:0], window[consumed:]...)
			processed += int64(n)

			if opts.Progress != nil {
				opts.Progress(Progress{
					BytesProcessed: processed,
					BytesTotal:     total,
					Matches:        matches,
				})
			}
		}

		if errors.Is(readErr, io.EOF) {
			if len(carry) > 0 {
				_, count, conflictCount, err := writeBatchPrefix(dst, carry, len(carry), total, processed-int64(len(carry)), compiled, opts.CaseInsensitive, opts.WholeWord)
				if err != nil {
					return matches, conflicts, err
				}
				matches += int64(count)
				conflicts += int64(conflictCount)
			}
			return matches, conflicts, dst.Sync()
		}
		if readErr != nil {
			return matches, conflicts, readErr
		}
	}
}

// PreviewBatchPlain returns bounded previews for the first batch matches.
func PreviewBatchPlain(ctx context.Context, r ReaderAtSize, rules []BatchRule, opts PreviewOptions) ([]BatchPreview, int64, error) {
	compiled, maxPattern, err := compileBatchRules(rules, opts.CaseInsensitive)
	if err != nil {
		return nil, 0, err
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 32 * 1024 * 1024
	}
	if opts.WholeWord && opts.ChunkSize < maxPattern+2 {
		opts.ChunkSize = maxPattern + 2
	}
	radius := opts.PreviewBytes
	if radius <= 0 {
		radius = 48
	}

	matches, conflicts, err := collectBatchMatches(ctx, r, compiled, opts.MaxHits, opts.ChunkSize, opts.CaseInsensitive, opts.WholeWord, maxPattern)
	if err != nil {
		return nil, 0, err
	}

	previews := make([]BatchPreview, 0, len(matches))
	for _, match := range matches {
		before, after, err := replacementSnippet(r, match.Offset, match.Length, match.Rule.Replace, radius)
		if err != nil {
			return nil, 0, err
		}
		previews = append(previews, BatchPreview{
			Offset:    match.Offset,
			RuleName:  match.Rule.Name,
			Before:    before,
			After:     after,
			Conflicts: match.Conflicts,
		})
	}
	return previews, conflicts, nil
}

// ReplaceBatchPlainFile streams sourcePath into outputPath while applying batch rules.
func ReplaceBatchPlainFile(ctx context.Context, sourcePath string, outputPath string, rules []BatchRule, opts FileOptions, batchOpts BatchOptions) (FileSummary, error) {
	same, err := samePath(sourcePath, outputPath)
	if err != nil {
		return FileSummary{}, err
	}
	if same {
		return FileSummary{}, errors.New("output path must be different from source path")
	}

	summary := FileSummary{
		OutputPath:   outputPath,
		TempPath:     outputPath + ".quarry.tmp",
		ManifestPath: outputPath + ".quarry.manifest.json",
	}

	if _, err := statPath(outputPath); err == nil {
		return summary, errors.New("output file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return summary, err
	}

	backupPath := opts.BackupPath
	if opts.SwapOriginal {
		if backupPath == "" {
			backupPath = sourcePath + ".quarry.bak"
		}
		sameBackup, err := samePath(sourcePath, backupPath)
		if err != nil {
			return summary, err
		}
		if sameBackup {
			return summary, errors.New("backup path must be different from source path")
		}
		if _, err := statPath(backupPath); err == nil {
			return summary, errors.New("backup file already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return summary, err
		}
		summary.BackupPath = backupPath
	}

	src, err := openSourceFile(sourcePath)
	if err != nil {
		return summary, err
	}
	srcClosed := false
	defer func() {
		if !srcClosed {
			_ = src.Close()
		}
	}()

	st, err := src.Stat()
	if err != nil {
		return summary, err
	}
	sourceState := snapshotSource(st)

	dst, err := openExclusive(summary.TempPath)
	if err != nil {
		return summary, err
	}

	manifest := Manifest{
		Operation:     "batch-plain-replace",
		Source:        sourcePath,
		Output:        outputPath,
		TempOutput:    summary.TempPath,
		Phase:         "processing",
		StartedAt:     time.Now().UTC(),
		SourceSize:    st.Size(),
		SourceModTime: st.ModTime().UnixNano(),
		BackupPlanned: backupPath,
		SwapRequested: opts.SwapOriginal,
		Status:        "running",
	}
	if err := writeManifest(summary.ManifestPath, manifest, true); err != nil {
		_ = dst.Close()
		_ = removePath(summary.TempPath)
		return summary, err
	}

	innerProgress := batchOpts.Progress
	batchOpts.Progress = func(p Progress) {
		manifest.BytesProcessed = p.BytesProcessed
		manifest.Matches = p.Matches
		if innerProgress != nil {
			innerProgress(p)
		}
		if opts.Progress != nil {
			opts.Progress(p)
		}
	}
	matches, conflicts, replaceErr := ReplaceBatchPlain(ctx, src, dst, rules, batchOpts)
	manifest.Matches = matches
	manifest.ConflictCount = conflicts
	closeErr := dst.Close()

	if replaceErr != nil {
		failFileTransformWrite(summary, &manifest, opts, replaceErr)
		return summary, replaceErr
	}
	if closeErr != nil {
		writeFailedManifest(summary.ManifestPath, &manifest, closeErr)
		return summary, closeErr
	}
	if err := writeReadyToFinalizeManifestOrFail(summary.ManifestPath, &manifest); err != nil {
		return summary, err
	}

	if err := renamePath(summary.TempPath, outputPath); err != nil {
		writeFailedManifest(summary.ManifestPath, &manifest, err)
		return summary, err
	}
	manifest.Phase = "output_written"

	if opts.SwapOriginal {
		if err := verifySourceUnchanged(sourcePath, sourceState); err != nil {
			writeFailedManifest(summary.ManifestPath, &manifest, err)
			return summary, err
		}
		if err := src.Close(); err != nil {
			srcClosed = true
			writeFailedManifest(summary.ManifestPath, &manifest, err)
			return summary, err
		}
		srcClosed = true
		if err := swapOutputIntoSource(sourcePath, outputPath, backupPath); err != nil {
			writeFailedManifest(summary.ManifestPath, &manifest, err)
			return summary, err
		}
		manifest.Backup = backupPath
		manifest.Swapped = true
		manifest.Phase = "swapped"
		summary.BackupPath = backupPath
		summary.Swapped = true
	}

	if err := writeCompletedManifestOrFail(summary.ManifestPath, &manifest, st.Size()); err != nil {
		return summary, err
	}

	summary.Matches = matches
	summary.Conflicts = conflicts
	return summary, nil
}

type batchMatch struct {
	Offset    int64
	Length    int
	Rule      compiledBatchRule
	Conflicts int
}

func collectBatchMatches(ctx context.Context, r ReaderAtSize, rules []compiledBatchRule, maxHits int, chunkSize int, caseInsensitive bool, wholeWord bool, maxPattern int) ([]batchMatch, int64, error) {
	keepSize := maxPattern - 1
	if wholeWord {
		keepSize = maxPattern + 2
	}
	buf := make([]byte, chunkSize)
	carry := make([]byte, 0, keepSize)
	window := make([]byte, 0, chunkSize+keepSize)
	processed := int64(0)
	size := r.Size()
	results := make([]batchMatch, 0, maxHits)
	var conflicts int64

	for processed < size {
		select {
		case <-ctx.Done():
			return nil, conflicts, ctx.Err()
		default:
		}

		want := chunkSize
		if remaining := size - processed; remaining < int64(want) {
			want = int(remaining)
		}
		n, readErr := r.ReadAt(buf[:want], processed)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, conflicts, readErr
		}
		if n == 0 {
			break
		}

		window = window[:0]
		window = append(window, carry...)
		window = append(window, buf[:n]...)
		processLimit := len(window) - keepSize
		if errors.Is(readErr, io.EOF) {
			processLimit = len(window)
		}
		if processLimit < 0 {
			processLimit = 0
		}

		windowStart := processed - int64(len(carry))
		consumed, foundMatches, conflictCount := collectBatchPrefix(window, processLimit, rules, caseInsensitive, wholeWord, windowStart, size, maxHits-len(results))
		conflicts += int64(conflictCount)
		results = append(results, foundMatches...)
		if maxHits > 0 && len(results) >= maxHits {
			return results, conflicts, nil
		}
		carry = append(carry[:0], window[consumed:]...)
		processed += int64(n)
	}

	if len(carry) > 0 {
		windowStart := processed - int64(len(carry))
		_, foundMatches, conflictCount := collectBatchPrefix(carry, len(carry), rules, caseInsensitive, wholeWord, windowStart, size, maxHits-len(results))
		conflicts += int64(conflictCount)
		results = append(results, foundMatches...)
	}

	return results, conflicts, nil
}

func collectBatchPrefix(window []byte, processLimit int, rules []compiledBatchRule, caseInsensitive bool, wholeWord bool, windowStart int64, size int64, remaining int) (int, []batchMatch, int) {
	cursor := 0
	consumed := processLimit
	conflicts := 0
	found := make([]batchMatch, 0, max(remaining, 0))
	for cursor < processLimit {
		candidate, ok := nextBatchCandidate(window, rules, cursor, processLimit, caseInsensitive, wholeWord, windowStart, size)
		if !ok {
			break
		}
		if candidate.start >= processLimit {
			break
		}
		if candidate.end > processLimit {
			safeStart := candidate.start
			if wholeWord && safeStart > 0 {
				safeStart--
			}
			consumed = safeStart
			break
		}
		conflictCount := countBatchConflicts(window, rules, candidate, caseInsensitive, wholeWord, windowStart, size)
		conflicts += conflictCount
		found = append(found, batchMatch{
			Offset:    windowStart + int64(candidate.start),
			Length:    len(candidate.rule.Find),
			Rule:      candidate.rule,
			Conflicts: conflictCount,
		})
		if remaining > 0 && len(found) >= remaining {
			return candidate.end, found, conflicts
		}
		cursor = candidate.end
	}
	if cursor > consumed {
		consumed = cursor
	}
	return consumed, found, conflicts
}

func writeBatchPrefix(dst io.Writer, window []byte, processLimit int, size int64, windowStart int64, rules []compiledBatchRule, caseInsensitive bool, wholeWord bool) (int, int, int, error) {
	cursor := 0
	matches := 0
	conflicts := 0
	for cursor < processLimit {
		candidate, found := nextBatchCandidate(window, rules, cursor, processLimit, caseInsensitive, wholeWord, windowStart, size)
		if !found {
			break
		}
		if candidate.start >= processLimit {
			break
		}
		if candidate.end > processLimit {
			safeStart := candidate.start
			if wholeWord && safeStart > 0 {
				safeStart--
			}
			if safeStart > cursor {
				if _, err := dst.Write(window[cursor:safeStart]); err != nil {
					return 0, matches, conflicts, err
				}
			}
			return safeStart, matches, conflicts, nil
		}
		if candidate.start > cursor {
			if _, err := dst.Write(window[cursor:candidate.start]); err != nil {
				return 0, matches, conflicts, err
			}
		}
		if _, err := dst.Write(candidate.rule.Replace); err != nil {
			return 0, matches, conflicts, err
		}
		conflicts += countBatchConflicts(window, rules, candidate, caseInsensitive, wholeWord, windowStart, size)
		matches++
		cursor = candidate.end
	}
	if cursor < processLimit {
		if _, err := dst.Write(window[cursor:processLimit]); err != nil {
			return 0, matches, conflicts, err
		}
		cursor = processLimit
	}
	return cursor, matches, conflicts, nil
}

func nextBatchCandidate(window []byte, rules []compiledBatchRule, start int, processLimit int, caseInsensitive bool, wholeWord bool, windowStart int64, size int64) (batchCandidate, bool) {
	var chosen batchCandidate
	found := false
	for _, rule := range rules {
		pos := start
		for pos < len(window) {
			idx := indexPlain(window[pos:], rule.needle, caseInsensitive)
			if idx < 0 {
				break
			}
			matchStart := pos + idx
			matchEnd := matchStart + len(rule.Find)
			if replaceWordBoundaryOK(window, matchStart, len(rule.Find), windowStart, size, wholeWord) {
				candidate := batchCandidate{start: matchStart, end: matchEnd, rule: rule}
				if !found || betterBatchCandidate(candidate, chosen) {
					chosen = candidate
					found = true
				}
				break
			}
			pos = matchStart + 1
		}
	}
	if !found {
		return batchCandidate{}, false
	}
	if chosen.start < processLimit && chosen.end > processLimit {
		return chosen, true
	}
	return chosen, true
}

func betterBatchCandidate(a batchCandidate, b batchCandidate) bool {
	if a.start != b.start {
		return a.start < b.start
	}
	if a.rule.Priority != b.rule.Priority {
		return a.rule.Priority < b.rule.Priority
	}
	if (a.end - a.start) != (b.end - b.start) {
		return (a.end - a.start) > (b.end - b.start)
	}
	return a.rule.order < b.rule.order
}

func countBatchConflicts(window []byte, rules []compiledBatchRule, chosen batchCandidate, caseInsensitive bool, wholeWord bool, windowStart int64, size int64) int {
	conflicts := 0
	for _, rule := range rules {
		pos := chosen.start
		for pos < len(window) {
			idx := indexPlain(window[pos:], rule.needle, caseInsensitive)
			if idx < 0 {
				break
			}
			matchStart := pos + idx
			matchEnd := matchStart + len(rule.Find)
			if matchStart >= chosen.end {
				break
			}
			if !replaceWordBoundaryOK(window, matchStart, len(rule.Find), windowStart, size, wholeWord) {
				pos = matchStart + 1
				continue
			}
			sameMatch := matchStart == chosen.start && matchEnd == chosen.end && rule.order == chosen.rule.order
			if !sameMatch && matchEnd > chosen.start {
				conflicts++
			}
			pos = matchStart + 1
		}
	}
	return conflicts
}

func compileBatchRules(rules []BatchRule, caseInsensitive bool) ([]compiledBatchRule, int, error) {
	if len(rules) == 0 {
		return nil, 0, errors.New("no batch rules")
	}
	compiled := make([]compiledBatchRule, 0, len(rules))
	maxPattern := 0
	for i, rule := range rules {
		if len(rule.Find) == 0 {
			return nil, 0, fmt.Errorf("rule %d has empty search text", i+1)
		}
		if rule.Name == "" {
			rule.Name = fmt.Sprintf("Rule %d", i+1)
		}
		if rule.Priority == 0 && i > 0 {
			rule.Priority = i
		}
		needle := rule.Find
		if caseInsensitive {
			needle = asciifold.Fold(rule.Find)
		}
		compiled = append(compiled, compiledBatchRule{
			BatchRule: rule,
			order:     i,
			needle:    needle,
		})
		if len(rule.Find) > maxPattern {
			maxPattern = len(rule.Find)
		}
	}
	return compiled, maxPattern, nil
}
