package replace

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// BatchRule is one plain-text batch replacement rule. When matches overlap,
// Novera chooses the earliest match; ties use lower Priority, then longer match,
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
	WriteBufferSize int
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
	order int
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
	if len(text) > MaxBatchRuleFileBytes {
		return BatchRuleSet{}, batchRuleLimit("rule text bytes", len(text), MaxBatchRuleFileBytes)
	}
	return parseBatchRuleReader(context.Background(), strings.NewReader(text))
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
	writeBufferSize, err := batchWriteBufferSize(opts.WriteBufferSize)
	if err != nil {
		return 0, 0, err
	}
	chunkSize, err := normalizedPlainChunkSize(opts.ChunkSize, 64*1024*1024, 1)
	if err != nil {
		return 0, 0, err
	}
	compiled, maxPattern, err := compileBatchRules(rules, opts.CaseInsensitive)
	if err != nil {
		return 0, 0, err
	}
	minimumChunk := 1
	if opts.WholeWord {
		minimumChunk = maxPattern + utf8.UTFMax
	}
	chunkSize, err = normalizedPlainChunkSize(chunkSize, 64*1024*1024, minimumChunk)
	if err != nil {
		return 0, 0, err
	}
	opts.ChunkSize = chunkSize

	st, err := src.Stat()
	if err != nil {
		return 0, 0, err
	}

	total := st.Size()
	buf := make([]byte, opts.ChunkSize)
	bufferedDst := bufio.NewWriterSize(dst, writeBufferSize)
	keepSize := maxPattern - 1
	if opts.WholeWord {
		keepSize = maxPattern + utf8.UTFMax
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

			consumed, count, conflictCount, err := writeBatchPrefix(ctx, bufferedDst, window, processLimit, total, processed-int64(len(carry)), compiled, opts.WholeWord)
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
				_, count, conflictCount, err := writeBatchPrefix(ctx, bufferedDst, carry, len(carry), total, processed-int64(len(carry)), compiled, opts.WholeWord)
				if err != nil {
					return matches, conflicts, err
				}
				matches += int64(count)
				conflicts += int64(conflictCount)
			}
			return matches, conflicts, finishRegexOutput(ctx, bufferedDst, dst)
		}
		if readErr != nil {
			return matches, conflicts, readErr
		}
	}
}

// PreviewBatchPlain returns bounded previews for the first batch matches.
func PreviewBatchPlain(ctx context.Context, r ReaderAtSize, rules []BatchRule, opts PreviewOptions) ([]BatchPreview, int64, error) {
	radius, err := validatePreviewBase(opts.ChunkSize, opts.MaxHits, opts.PreviewBytes)
	if err != nil {
		return nil, 0, err
	}
	compiled, maxPattern, err := compileBatchRules(rules, opts.CaseInsensitive)
	if err != nil {
		return nil, 0, err
	}
	_, maxReplacement := maxBatchPreviewDimensions(rules)
	if err := validatePreviewBudget(opts.MaxHits, radius, maxPattern, maxReplacement, 0); err != nil {
		return nil, 0, err
	}
	minimumChunk := 1
	if opts.WholeWord {
		minimumChunk = maxPattern + utf8.UTFMax
	}
	chunkSize, err := normalizedPlainChunkSize(opts.ChunkSize, 32*1024*1024, minimumChunk)
	if err != nil {
		return nil, 0, err
	}
	opts.ChunkSize = chunkSize

	matches, conflicts, err := collectBatchMatches(ctx, r, compiled, opts.MaxHits, opts.ChunkSize, opts.WholeWord, maxPattern)
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
	if opts.SwapOriginal {
		return FileSummary{}, ErrSwapOriginalDisabled
	}
	if _, err := batchWriteBufferSize(batchOpts.WriteBufferSize); err != nil {
		return FileSummary{OutputPath: outputPath}, err
	}
	if _, err := normalizedPlainChunkSize(batchOpts.ChunkSize, 64*1024*1024, 1); err != nil {
		return FileSummary{OutputPath: outputPath}, err
	}
	if _, _, err := compileBatchRules(rules, batchOpts.CaseInsensitive); err != nil {
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

type batchMatch struct {
	Offset    int64
	Length    int
	Rule      compiledBatchRule
	Conflicts int
}

func collectBatchMatches(ctx context.Context, r ReaderAtSize, rules *compiledBatchSet, maxHits int, chunkSize int, wholeWord bool, maxPattern int) ([]batchMatch, int64, error) {
	if maxHits <= 0 || maxHits > MaxPreviewHits {
		return nil, 0, replaceLimit("maximum preview hits must be between 1 and %d", MaxPreviewHits)
	}
	if chunkSize <= 0 || chunkSize > MaxPlainChunkBytes {
		return nil, 0, replaceLimit("preview chunk size must be between 1 and %d bytes", MaxPlainChunkBytes)
	}
	keepSize := maxPattern - 1
	if wholeWord {
		keepSize = maxPattern + utf8.UTFMax
	}
	buf := make([]byte, chunkSize)
	carry := make([]byte, 0, keepSize)
	window := make([]byte, 0, chunkSize+keepSize)
	processed := int64(0)
	size := r.Size()
	if size < 0 {
		return nil, 0, errors.New("source size must not be negative")
	}
	resultCapacity := maxHits
	if resultCapacity < 0 {
		resultCapacity = 0
	}
	results := make([]batchMatch, 0, resultCapacity)
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
		processLimit := len(window) - keepSize
		if atEnd {
			processLimit = len(window)
		}
		if processLimit < 0 {
			processLimit = 0
		}

		windowStart := processed - int64(len(carry))
		consumed, foundMatches, conflictCount, collectErr := collectBatchPrefix(ctx, window, processLimit, rules, wholeWord, windowStart, size, maxHits-len(results))
		if collectErr != nil {
			return nil, conflicts, collectErr
		}
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
		_, foundMatches, conflictCount, collectErr := collectBatchPrefix(ctx, carry, len(carry), rules, wholeWord, windowStart, size, maxHits-len(results))
		if collectErr != nil {
			return nil, conflicts, collectErr
		}
		conflicts += int64(conflictCount)
		results = append(results, foundMatches...)
	}

	return results, conflicts, nil
}

func collectBatchPrefix(ctx context.Context, window []byte, processLimit int, rules *compiledBatchSet, wholeWord bool, windowStart int64, size int64, remaining int) (int, []batchMatch, int, error) {
	capacity := remaining
	if capacity < 0 {
		capacity = 0
	}
	found := make([]batchMatch, 0, capacity)
	consumed, _, conflicts, err := arbitrateBatchPrefix(ctx, window, processLimit, size, windowStart, rules, wholeWord, remaining, func(candidate batchCandidate, conflictCount int) error {
		found = append(found, batchMatch{
			Offset:    windowStart + int64(candidate.start),
			Length:    len(candidate.rule.Find),
			Rule:      candidate.rule,
			Conflicts: conflictCount,
		})
		return nil
	})
	return consumed, found, conflicts, err
}

func writeBatchPrefix(ctx context.Context, dst io.Writer, window []byte, processLimit int, size int64, windowStart int64, rules *compiledBatchSet, wholeWord bool) (int, int, int, error) {
	writeAt := 0
	consumed, matches, conflicts, err := arbitrateBatchPrefix(ctx, window, processLimit, size, windowStart, rules, wholeWord, 0, func(candidate batchCandidate, _ int) error {
		if candidate.start > writeAt {
			if err := writeBatchBytes(dst, window[writeAt:candidate.start]); err != nil {
				return err
			}
		}
		if err := writeBatchBytes(dst, candidate.rule.Replace); err != nil {
			return err
		}
		writeAt = candidate.end
		return nil
	})
	if err != nil {
		return 0, matches, conflicts, err
	}
	if consumed > writeAt {
		if err := writeBatchBytes(dst, window[writeAt:consumed]); err != nil {
			return 0, matches, conflicts, err
		}
	}
	return consumed, matches, conflicts, nil
}

func writeBatchBytes(dst io.Writer, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	n, err := dst.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
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
