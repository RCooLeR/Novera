package replace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"
)

type compiledRegexBatchRule struct {
	BatchRule
	order int
	re    *regexp.Regexp
}

type regexBatchCandidate struct {
	start int
	end   int
	rule  compiledRegexBatchRule
	loc   []int
}

// PreviewBatchRegexp returns bounded previews for the first regex batch matches.
func PreviewBatchRegexp(ctx context.Context, r ReaderAtSize, rules []BatchRule, opts RegexPreviewOptions, regexOpts RegexOptions) ([]BatchPreview, int64, error) {
	compiled, err := compileRegexBatchRules(rules, regexOpts.CaseInsensitive)
	if err != nil {
		return nil, 0, err
	}
	regexOpts.ChunkSize = firstPositive(regexOpts.ChunkSize, opts.ChunkSize)
	regexOpts = normalizeRegexOptions(regexOpts)
	radius := opts.PreviewBytes
	if radius <= 0 {
		radius = 48
	}

	matches, conflicts, err := collectRegexBatchMatches(ctx, r, compiled, regexOpts, opts.MaxHits)
	if err != nil {
		return nil, 0, err
	}

	previews := make([]BatchPreview, 0, len(matches))
	for _, match := range matches {
		before, after, err := regexBatchReplacementSnippet(r, match.Offset, match.Length, match.rule, match.loc, radius)
		if err != nil {
			return nil, 0, err
		}
		previews = append(previews, BatchPreview{
			Offset:    match.Offset,
			RuleName:  match.rule.Name,
			Before:    before,
			After:     after,
			Conflicts: match.Conflicts,
		})
	}
	return previews, conflicts, nil
}

// ReplaceBatchRegexpFile streams sourcePath into outputPath while applying regex rules.
func ReplaceBatchRegexpFile(ctx context.Context, sourcePath string, outputPath string, rules []BatchRule, opts FileOptions, regexOpts RegexOptions) (FileSummary, error) {
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
		Operation:     "batch-regex-replace",
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

	innerProgress := regexOpts.Progress
	regexOpts.Progress = func(p Progress) {
		manifest.BytesProcessed = p.BytesProcessed
		manifest.Matches = p.Matches
		if innerProgress != nil {
			innerProgress(p)
		}
		if opts.Progress != nil {
			opts.Progress(p)
		}
	}
	matches, conflicts, replaceErr := ReplaceBatchRegexp(ctx, src, dst, rules, regexOpts)
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
	if err := writeManifestPhaseOrFail(summary.ManifestPath, &manifest, "output_written"); err != nil {
		return summary, err
	}

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
		summary.BackupPath = backupPath
		summary.Swapped = true
		if err := writeManifestPhaseOrFail(summary.ManifestPath, &manifest, "swapped"); err != nil {
			return summary, err
		}
	}

	if err := writeCompletedManifestOrFail(summary.ManifestPath, &manifest, st.Size()); err != nil {
		return summary, err
	}

	summary.Matches = matches
	summary.Conflicts = conflicts
	return summary, nil
}

// ReplaceBatchRegexp streams src to dst while applying regex rules in one pass.
func ReplaceBatchRegexp(ctx context.Context, src *os.File, dst syncWriter, rules []BatchRule, opts RegexOptions) (int64, int64, error) {
	compiled, err := compileRegexBatchRules(rules, opts.CaseInsensitive)
	if err != nil {
		return 0, 0, err
	}
	opts = normalizeRegexOptions(opts)

	st, err := src.Stat()
	if err != nil {
		return 0, 0, err
	}

	total := st.Size()
	buf := make([]byte, opts.ChunkSize)
	carryBudget := opts.MaxMatchWindow * 2
	carry := make([]byte, 0, carryBudget)
	window := make([]byte, 0, opts.ChunkSize+carryBudget)

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
			processLimit := len(window) - opts.MaxMatchWindow
			if errors.Is(readErr, io.EOF) {
				processLimit = len(window)
			}
			if processLimit < 0 {
				processLimit = 0
			}

			consumed, count, conflictCount, err := writeRegexBatchPrefix(dst, window, processLimit, compiled)
			if err != nil {
				return matches, conflicts, err
			}
			if len(window)-consumed > carryBudget {
				return matches, conflicts, ErrRegexMatchExceededWindow
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

		if errors.Is(readErr, io.EOF) && len(carry) > 0 {
			_, count, conflictCount, err := writeRegexBatchPrefix(dst, carry, len(carry), compiled)
			if err != nil {
				return matches, conflicts, err
			}
			matches += int64(count)
			conflicts += int64(conflictCount)
		}
		if errors.Is(readErr, io.EOF) {
			return matches, conflicts, dst.Sync()
		}
		if readErr != nil {
			return matches, conflicts, readErr
		}
	}
}

type regexBatchMatch struct {
	Offset    int64
	Length    int
	rule      compiledRegexBatchRule
	loc       []int
	Conflicts int
}

func collectRegexBatchMatches(ctx context.Context, r ReaderAtSize, rules []compiledRegexBatchRule, opts RegexOptions, maxHits int) ([]regexBatchMatch, int64, error) {
	opts = normalizeRegexOptions(opts)
	results := make([]regexBatchMatch, 0, maxHits)
	buf := make([]byte, opts.ChunkSize)
	carryBudget := opts.MaxMatchWindow * 2
	carry := make([]byte, 0, carryBudget)
	window := make([]byte, 0, opts.ChunkSize+carryBudget)
	processed := int64(0)

	var conflicts int64
	for processed < r.Size() {
		select {
		case <-ctx.Done():
			return nil, conflicts, ctx.Err()
		default:
		}

		want := opts.ChunkSize
		if remaining := r.Size() - processed; remaining < int64(want) {
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
		processLimit := len(window) - opts.MaxMatchWindow
		if errors.Is(readErr, io.EOF) {
			processLimit = len(window)
		}
		if processLimit < 0 {
			processLimit = 0
		}
		windowStart := processed - int64(len(carry))

		consumed, foundMatches, conflictCount, err := collectRegexBatchPrefix(window, processLimit, rules, windowStart, maxHits-len(results))
		if err != nil {
			return nil, conflicts, err
		}
		conflicts += int64(conflictCount)
		results = append(results, foundMatches...)
		if len(window)-consumed > carryBudget {
			return nil, conflicts, ErrRegexMatchExceededWindow
		}
		if maxHits > 0 && len(results) >= maxHits {
			return results, conflicts, nil
		}
		carry = append(carry[:0], window[consumed:]...)
		processed += int64(n)
	}

	if len(carry) > 0 {
		windowStart := processed - int64(len(carry))
		_, foundMatches, conflictCount, err := collectRegexBatchPrefix(carry, len(carry), rules, windowStart, maxHits-len(results))
		if err != nil {
			return nil, conflicts, err
		}
		conflicts += int64(conflictCount)
		results = append(results, foundMatches...)
	}

	return results, conflicts, nil
}

func writeRegexBatchPrefix(dst io.Writer, window []byte, processLimit int, rules []compiledRegexBatchRule) (int, int, int, error) {
	safeLimit, selected, conflicts, _ := resolveRegexBatchCandidates(window, processLimit, rules)
	cursor := 0
	for _, candidate := range selected {
		if candidate.start > cursor {
			if _, err := dst.Write(window[cursor:candidate.start]); err != nil {
				return 0, 0, 0, err
			}
		}
		if _, err := dst.Write(candidate.rule.re.Expand(nil, candidate.rule.Replace, window, candidate.loc)); err != nil {
			return 0, 0, 0, err
		}
		cursor = candidate.end
	}
	if cursor < safeLimit {
		if _, err := dst.Write(window[cursor:safeLimit]); err != nil {
			return 0, 0, 0, err
		}
	}
	return safeLimit, len(selected), conflicts, nil
}

func collectRegexBatchPrefix(window []byte, processLimit int, rules []compiledRegexBatchRule, windowStart int64, remaining int) (int, []regexBatchMatch, int, error) {
	safeLimit, selected, conflicts, allSafe := resolveRegexBatchCandidates(window, processLimit, rules)
	if remaining > 0 && len(selected) > remaining {
		selected = selected[:remaining]
	}
	found := make([]regexBatchMatch, 0, len(selected))
	for _, candidate := range selected {
		found = append(found, regexBatchMatch{
			Offset:    windowStart + int64(candidate.start),
			Length:    candidate.end - candidate.start,
			rule:      candidate.rule,
			loc:       append([]int(nil), candidate.loc...),
			Conflicts: countRegexBatchConflicts(allSafe, candidate),
		})
	}
	return safeLimit, found, conflicts, nil
}

func resolveRegexBatchCandidates(window []byte, processLimit int, rules []compiledRegexBatchRule) (int, []regexBatchCandidate, int, []regexBatchCandidate) {
	all := collectRegexBatchCandidates(window, rules)
	safeLimit := processLimit
	for _, candidate := range all {
		if candidate.start < processLimit && candidate.end > processLimit && candidate.start < safeLimit {
			safeLimit = candidate.start
		}
	}
	safe := make([]regexBatchCandidate, 0, len(all))
	for _, candidate := range all {
		if candidate.end <= safeLimit {
			safe = append(safe, candidate)
		}
	}
	selected, conflicts := selectRegexBatchCandidates(safe)
	return safeLimit, selected, conflicts, safe
}

func collectRegexBatchCandidates(window []byte, rules []compiledRegexBatchRule) []regexBatchCandidate {
	candidates := make([]regexBatchCandidate, 0)
	for _, rule := range rules {
		locs := rule.re.FindAllSubmatchIndex(window, -1)
		for _, loc := range locs {
			if len(loc) < 2 {
				continue
			}
			candidates = append(candidates, regexBatchCandidate{
				start: loc[0],
				end:   loc[1],
				rule:  rule,
				loc:   append([]int(nil), loc...),
			})
		}
	}
	return candidates
}

func selectRegexBatchCandidates(candidates []regexBatchCandidate) ([]regexBatchCandidate, int) {
	if len(candidates) == 0 {
		return nil, 0
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return betterRegexBatchCandidate(candidates[i], candidates[j])
	})

	selected := make([]regexBatchCandidate, 0, len(candidates))
	cursor := 0
	for _, candidate := range candidates {
		if candidate.start < cursor {
			continue
		}
		selected = append(selected, candidate)
		cursor = candidate.end
	}
	conflicts := 0
	for _, picked := range selected {
		conflicts += countRegexBatchConflicts(candidates, picked)
	}
	return selected, conflicts
}

func countRegexBatchConflicts(candidates []regexBatchCandidate, chosen regexBatchCandidate) int {
	conflicts := 0
	for _, candidate := range candidates {
		same := candidate.start == chosen.start && candidate.end == chosen.end && candidate.rule.order == chosen.rule.order
		if same {
			continue
		}
		if candidate.start < chosen.end && candidate.end > chosen.start {
			conflicts++
		}
	}
	return conflicts
}

func betterRegexBatchCandidate(a regexBatchCandidate, b regexBatchCandidate) bool {
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

func compileRegexBatchRules(rules []BatchRule, caseInsensitive bool) ([]compiledRegexBatchRule, error) {
	if len(rules) == 0 {
		return nil, errors.New("no batch rules")
	}
	compiled := make([]compiledRegexBatchRule, 0, len(rules))
	for i, rule := range rules {
		if len(rule.Find) == 0 {
			return nil, fmt.Errorf("rule %d has empty search text", i+1)
		}
		if rule.Name == "" {
			rule.Name = fmt.Sprintf("Rule %d", i+1)
		}
		if rule.Priority == 0 && i > 0 {
			rule.Priority = i
		}
		re, err := compileRegex(rule.Find, caseInsensitive)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rule.Name, err)
		}
		compiled = append(compiled, compiledRegexBatchRule{
			BatchRule: rule,
			order:     i,
			re:        re,
		})
	}
	return compiled, nil
}

func regexBatchReplacementSnippet(r ReaderAtSize, offset int64, length int, rule compiledRegexBatchRule, loc []int, radius int) (string, string, error) {
	start := offset - int64(radius)
	if start < 0 {
		start = 0
	}
	end := offset + int64(length+radius)
	if size := r.Size(); end > size {
		end = size
	}

	buf := make([]byte, end-start)
	n, err := r.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", err
	}
	buf = buf[:n]

	localStart := int(offset - start)
	localEnd := localStart + length
	if localStart < 0 || localEnd > len(buf) {
		return "", "", errors.New("preview window does not contain regex batch match")
	}

	matchBytes := buf[localStart:localEnd]
	matchLoc := rule.re.FindSubmatchIndex(matchBytes)
	if matchLoc == nil || matchLoc[0] != 0 || matchLoc[1] != len(matchBytes) {
		if len(loc) > 0 {
			matchLoc = normalizeRegexLoc(loc, localStart)
		}
	}
	if matchLoc == nil || matchLoc[0] != 0 || matchLoc[1] != len(matchBytes) {
		return "", "", errors.New("preview window could not re-expand regex batch match")
	}

	var after bytes.Buffer
	after.Write(buf[:localStart])
	after.Write(rule.re.Expand(nil, rule.Replace, matchBytes, matchLoc))
	after.Write(buf[localEnd:])

	return cleanSnippet(buf), cleanSnippet(after.Bytes()), nil
}

func normalizeRegexLoc(loc []int, base int) []int {
	out := make([]int, len(loc))
	for i, value := range loc {
		if value < 0 {
			out[i] = value
			continue
		}
		out[i] = value - base
	}
	return out
}
