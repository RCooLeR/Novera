package replace

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"
)

type compiledRegexBatchRule struct {
	BatchRule
	order                       int
	re                          *regexp.Regexp
	maxMatchBytes               int
	maxExpandedReplacementBytes int
}

type regexBatchCandidate struct {
	start     int
	end       int
	rule      compiledRegexBatchRule
	loc       []int
	iterator  int
	conflicts int
}

type regexBatchIterator struct {
	window []byte
	rule   compiledRegexBatchRule
	pos    int
}

func (it *regexBatchIterator) next(iterator int) (regexBatchCandidate, bool) {
	if it.pos > len(it.window) {
		return regexBatchCandidate{}, false
	}
	loc := it.rule.re.FindSubmatchIndex(it.window[it.pos:])
	if len(loc) < 2 {
		return regexBatchCandidate{}, false
	}
	start := it.pos + loc[0]
	end := it.pos + loc[1]
	for i, value := range loc {
		if value >= 0 {
			loc[i] = value + it.pos
		}
	}
	if end > start {
		it.pos = end
	} else {
		it.pos = end + 1
	}
	return regexBatchCandidate{
		start:    start,
		end:      end,
		rule:     it.rule,
		loc:      loc,
		iterator: iterator,
	}, true
}

type regexBatchCandidateHeap []regexBatchCandidate

func (h regexBatchCandidateHeap) Len() int { return len(h) }
func (h regexBatchCandidateHeap) Less(i int, j int) bool {
	return betterRegexBatchCandidate(h[i], h[j])
}
func (h regexBatchCandidateHeap) Swap(i int, j int) { h[i], h[j] = h[j], h[i] }
func (h *regexBatchCandidateHeap) Push(value any) {
	*h = append(*h, value.(regexBatchCandidate))
}
func (h *regexBatchCandidateHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	old[last] = regexBatchCandidate{}
	*h = old[:last]
	return value
}

type regexBatchConflictShadow struct {
	start int64
	end   int64
}

type regexBatchConflictHeap []regexBatchConflictShadow

func (h regexBatchConflictHeap) Len() int               { return len(h) }
func (h regexBatchConflictHeap) Less(i int, j int) bool { return h[i].end < h[j].end }
func (h regexBatchConflictHeap) Swap(i int, j int)      { h[i], h[j] = h[j], h[i] }
func (h *regexBatchConflictHeap) Push(value any) {
	*h = append(*h, value.(regexBatchConflictShadow))
}
func (h *regexBatchConflictHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	old[last] = regexBatchConflictShadow{}
	*h = old[:last]
	return value
}

type regexBatchConflictState struct {
	shadows    regexBatchConflictHeap
	ruleCursor []int64
}

type regexBatchCandidateStream struct {
	iterators []regexBatchIterator
	pending   regexBatchCandidateHeap
}

func newRegexBatchCandidateStream(window []byte, windowStart int64, rules []compiledRegexBatchRule, ruleCursor []int64) *regexBatchCandidateStream {
	stream := &regexBatchCandidateStream{
		iterators: make([]regexBatchIterator, len(rules)),
		pending:   make(regexBatchCandidateHeap, 0, len(rules)),
	}
	for i, rule := range rules {
		pos := 0
		if ruleCursor[i] > windowStart {
			delta := ruleCursor[i] - windowStart
			if delta > int64(len(window)) {
				pos = len(window) + 1
			} else {
				pos = int(delta)
			}
		}
		stream.iterators[i] = regexBatchIterator{window: window, rule: rule, pos: pos}
		if candidate, ok := stream.iterators[i].next(i); ok {
			stream.pending = append(stream.pending, candidate)
		}
	}
	heap.Init(&stream.pending)
	return stream
}

func (s *regexBatchCandidateStream) pop() regexBatchCandidate {
	candidate := heap.Pop(&s.pending).(regexBatchCandidate)
	if next, ok := s.iterators[candidate.iterator].next(candidate.iterator); ok {
		heap.Push(&s.pending, next)
	}
	return candidate
}

// PreviewBatchRegexp returns bounded previews for the first regex batch matches.
func PreviewBatchRegexp(ctx context.Context, r ReaderAtSize, rules []BatchRule, opts RegexPreviewOptions, regexOpts RegexOptions) ([]BatchPreview, int64, error) {
	radius, err := validatePreviewBase(opts.ChunkSize, opts.MaxHits, opts.PreviewBytes)
	if err != nil {
		return nil, 0, err
	}
	if err := validateRegexOptions(regexOpts); err != nil {
		return nil, 0, err
	}
	regexOpts.ChunkSize = firstPositive(regexOpts.ChunkSize, opts.ChunkSize)
	regexOpts = normalizeRegexOptions(regexOpts)
	compiled, err := compileRegexBatchRules(rules, regexOpts.CaseInsensitive, regexOpts.MaxMatchWindow)
	if err != nil {
		return nil, 0, err
	}
	maxMatchBytes := 0
	maxReplacement := 0
	maxCaptureSlots := 0
	for _, rule := range compiled {
		if rule.maxMatchBytes > maxMatchBytes {
			maxMatchBytes = rule.maxMatchBytes
		}
		if rule.maxExpandedReplacementBytes > maxReplacement {
			maxReplacement = rule.maxExpandedReplacementBytes
		}
		if slots := 2 * (rule.re.NumSubexp() + 1); slots > maxCaptureSlots {
			maxCaptureSlots = slots
		}
	}
	if err := validatePreviewBudget(opts.MaxHits, radius, maxMatchBytes, maxReplacement, maxCaptureSlots); err != nil {
		return nil, 0, err
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
	if opts.SwapOriginal {
		return FileSummary{}, ErrSwapOriginalDisabled
	}
	if err := validateRegexOptions(regexOpts); err != nil {
		return FileSummary{OutputPath: outputPath}, err
	}
	regexOpts = normalizeRegexOptions(regexOpts)
	if _, err := compileRegexBatchRules(rules, regexOpts.CaseInsensitive, regexOpts.MaxMatchWindow); err != nil {
		return FileSummary{OutputPath: outputPath}, err
	}
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
	if err := validateRegexOptions(opts); err != nil {
		return 0, 0, err
	}
	opts = normalizeRegexOptions(opts)
	compiled, err := compileRegexBatchRules(rules, opts.CaseInsensitive, opts.MaxMatchWindow)
	if err != nil {
		return 0, 0, err
	}

	st, err := src.Stat()
	if err != nil {
		return 0, 0, err
	}

	total := st.Size()
	buf := make([]byte, opts.ChunkSize)
	bufferedDst := bufio.NewWriterSize(dst, opts.WriteBufferSize)
	carryBudget := opts.MaxMatchWindow * 2
	carry := make([]byte, 0, carryBudget)
	window := make([]byte, 0, opts.ChunkSize+carryBudget)

	var processed int64
	var matches int64
	var conflicts int64
	conflictState := regexBatchConflictState{}

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

			windowStart := processed - int64(len(carry))
			consumed, count, conflictCount, err := writeRegexBatchPrefix(ctx, bufferedDst, window, processLimit, compiled, windowStart, &conflictState)
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
			windowStart := processed - int64(len(carry))
			_, count, conflictCount, err := writeRegexBatchPrefix(ctx, bufferedDst, carry, len(carry), compiled, windowStart, &conflictState)
			if err != nil {
				return matches, conflicts, err
			}
			matches += int64(count)
			conflicts += int64(conflictCount)
		}
		if errors.Is(readErr, io.EOF) {
			return matches, conflicts, finishRegexOutput(ctx, bufferedDst, dst)
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
	if err := validateRegexOptions(opts); err != nil {
		return nil, 0, err
	}
	if maxHits <= 0 || maxHits > MaxPreviewHits {
		return nil, 0, replaceLimit("maximum preview hits must be between 1 and %d", MaxPreviewHits)
	}
	opts = normalizeRegexOptions(opts)
	size := r.Size()
	if size < 0 {
		return nil, 0, errors.New("source size must not be negative")
	}
	results := make([]regexBatchMatch, 0, maxHits)
	buf := make([]byte, opts.ChunkSize)
	carryBudget := opts.MaxMatchWindow * 2
	carry := make([]byte, 0, carryBudget)
	window := make([]byte, 0, opts.ChunkSize+carryBudget)
	processed := int64(0)
	conflictState := regexBatchConflictState{}

	var conflicts int64
	for processed < size {
		select {
		case <-ctx.Done():
			return nil, conflicts, ctx.Err()
		default:
		}

		want := opts.ChunkSize
		if remaining := size - processed; remaining < int64(want) {
			want = int(remaining)
		}
		n, readErr := r.ReadAt(buf[:want], processed)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, conflicts, readErr
		}
		if n != want {
			return nil, conflicts, io.ErrUnexpectedEOF
		}
		atEnd := processed+int64(n) == size
		if errors.Is(readErr, io.EOF) && !atEnd {
			return nil, conflicts, io.ErrUnexpectedEOF
		}

		window = window[:0]
		window = append(window, carry...)
		window = append(window, buf[:n]...)
		processLimit := len(window) - opts.MaxMatchWindow
		if atEnd {
			processLimit = len(window)
		}
		if processLimit < 0 {
			processLimit = 0
		}
		windowStart := processed - int64(len(carry))

		consumed, foundMatches, conflictCount, err := collectRegexBatchPrefix(ctx, window, processLimit, rules, windowStart, maxHits-len(results), &conflictState)
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
		_, foundMatches, conflictCount, err := collectRegexBatchPrefix(ctx, carry, len(carry), rules, windowStart, maxHits-len(results), &conflictState)
		if err != nil {
			return nil, conflicts, err
		}
		conflicts += int64(conflictCount)
		results = append(results, foundMatches...)
	}

	return results, conflicts, nil
}

func writeRegexBatchPrefix(ctx context.Context, dst io.Writer, window []byte, processLimit int, rules []compiledRegexBatchRule, windowStart int64, conflictState *regexBatchConflictState) (int, int, int, error) {
	cursor := 0
	safeLimit, count, conflicts, err := streamRegexBatchPrefix(ctx, window, processLimit, rules, windowStart, conflictState, func(candidate regexBatchCandidate) error {
		if candidate.start > cursor {
			if _, err := dst.Write(window[cursor:candidate.start]); err != nil {
				return err
			}
		}
		if _, err := dst.Write(candidate.rule.re.Expand(nil, candidate.rule.Replace, window, candidate.loc)); err != nil {
			return err
		}
		cursor = candidate.end
		return nil
	})
	if err != nil {
		return 0, count, conflicts, err
	}
	if cursor < safeLimit {
		if _, err := dst.Write(window[cursor:safeLimit]); err != nil {
			return 0, count, conflicts, err
		}
	}
	return safeLimit, count, conflicts, nil
}

func collectRegexBatchPrefix(ctx context.Context, window []byte, processLimit int, rules []compiledRegexBatchRule, windowStart int64, remaining int, conflictState *regexBatchConflictState) (int, []regexBatchMatch, int, error) {
	capacity := remaining
	if capacity < 0 {
		capacity = 0
	}
	found := make([]regexBatchMatch, 0, capacity)
	safeLimit, _, conflicts, err := streamRegexBatchPrefix(ctx, window, processLimit, rules, windowStart, conflictState, func(candidate regexBatchCandidate) error {
		if remaining > 0 && len(found) >= remaining {
			return errRegexBatchPreviewLimitReached
		}
		found = append(found, regexBatchMatch{
			Offset:    windowStart + int64(candidate.start),
			Length:    candidate.end - candidate.start,
			rule:      candidate.rule,
			loc:       append([]int(nil), candidate.loc...),
			Conflicts: candidate.conflicts,
		})
		return nil
	})
	if errors.Is(err, errRegexBatchPreviewLimitReached) {
		err = nil
	}
	return safeLimit, found, conflicts, err
}

var errRegexBatchPreviewLimitReached = errors.New("regex batch preview limit reached")

// streamRegexBatchPrefix merges one pending match per rule. Memory is bounded
// by the rule count instead of the number of matches in a dense source window.
func streamRegexBatchPrefix(ctx context.Context, window []byte, processLimit int, rules []compiledRegexBatchRule, windowStart int64, conflictState *regexBatchConflictState, emit func(regexBatchCandidate) error) (safeLimit int, count int, conflicts int, err error) {
	if processLimit < 0 || processLimit > len(window) {
		return 0, 0, 0, errors.New("invalid regex batch process limit")
	}
	if conflictState == nil {
		return 0, 0, 0, errors.New("nil regex batch conflict state")
	}
	if len(conflictState.ruleCursor) == 0 {
		conflictState.ruleCursor = make([]int64, len(rules))
	} else if len(conflictState.ruleCursor) != len(rules) {
		return 0, 0, 0, errors.New("regex batch rule state does not match compiled rules")
	}
	stream := newRegexBatchCandidateStream(window, windowStart, rules, conflictState.ruleCursor)
	skipped := &conflictState.shadows
	heap.Init(skipped)
	safeLimit = processLimit
	work := 0

	for stream.pending.Len() > 0 {
		work++
		if work%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return safeLimit, count, conflicts, err
			}
		}
		chosen := stream.pop()
		if chosen.start >= processLimit {
			break
		}
		if chosen.end > processLimit {
			safeLimit = chosen.start
			break
		}
		absoluteStart := windowStart + int64(chosen.start)
		absoluteEnd := windowStart + int64(chosen.end)
		if absoluteEnd > conflictState.ruleCursor[chosen.iterator] {
			conflictState.ruleCursor[chosen.iterator] = absoluteEnd
		}

		for skipped.Len() > 0 && (*skipped)[0].end <= absoluteStart {
			heap.Pop(skipped)
		}
		chosen.conflicts = skipped.Len()

		for stream.pending.Len() > 0 && stream.pending[0].start < chosen.end {
			work++
			if work%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return safeLimit, count, conflicts, err
				}
			}
			loser := stream.pop()
			chosen.conflicts++
			loserEnd := windowStart + int64(loser.end)
			if loserEnd > conflictState.ruleCursor[loser.iterator] {
				conflictState.ruleCursor[loser.iterator] = loserEnd
			}
			if loser.end > chosen.end {
				heap.Push(skipped, regexBatchConflictShadow{
					start: windowStart + int64(loser.start),
					end:   loserEnd,
				})
			}
		}
		if err := emit(chosen); err != nil {
			return safeLimit, count, conflicts, err
		}
		count++
		conflicts += chosen.conflicts
	}

	absoluteSafeLimit := windowStart + int64(safeLimit)
	retained := (*skipped)[:0]
	for _, shadow := range *skipped {
		if shadow.start < absoluteSafeLimit && shadow.end > absoluteSafeLimit {
			retained = append(retained, shadow)
		}
	}
	*skipped = retained
	heap.Init(skipped)
	return safeLimit, count, conflicts, nil
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

func compileRegexBatchRules(rules []BatchRule, caseInsensitive bool, maxMatchWindow int) ([]compiledRegexBatchRule, error) {
	if _, err := validateBatchRules(rules); err != nil {
		return nil, err
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
		re, analysis, err := compileRegexBoundedAnalysis(rule.Find, caseInsensitive, maxMatchWindow)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rule.Name, err)
		}
		expandedReplacementBytes, err := validateRegexReplacementExpansion(rule.Replace, analysis.MaxMatchBytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rule.Name, err)
		}
		compiled = append(compiled, compiledRegexBatchRule{
			BatchRule:                   rule,
			order:                       i,
			re:                          re,
			maxMatchBytes:               int(analysis.MaxMatchBytes),
			maxExpandedReplacementBytes: expandedReplacementBytes,
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
