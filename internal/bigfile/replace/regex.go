package replace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"time"

	"novera/internal/bigfile/regexutil"
)

const defaultRegexMatchWindow = 1 * 1024 * 1024

// RegexOptions controls bounded regex search/replace operations.
type RegexOptions struct {
	ChunkSize       int
	MaxMatchWindow  int
	CaseInsensitive bool
	Progress        func(Progress)
}

// ErrRegexMatchExceededWindow reports that a regex match could not be finalized
// inside the configured bounded overlap window.
var ErrRegexMatchExceededWindow = errors.New("regex match exceeded the configured match window")

// ReplaceRegexp streams src to dst while applying regex replacements.
func ReplaceRegexp(ctx context.Context, src *os.File, dst syncWriter, pattern []byte, repl []byte, opts RegexOptions) (int64, error) {
	re, err := compileRegex(pattern, opts.CaseInsensitive)
	if err != nil {
		return 0, err
	}

	opts = normalizeRegexOptions(opts)
	st, err := src.Stat()
	if err != nil {
		return 0, err
	}

	total := st.Size()
	buf := make([]byte, opts.ChunkSize)
	carryBudget := opts.MaxMatchWindow * 2
	carry := make([]byte, 0, carryBudget)
	window := make([]byte, 0, opts.ChunkSize+carryBudget)

	var processed int64
	var matches int64

	for {
		select {
		case <-ctx.Done():
			return matches, ctx.Err()
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

			consumed, count, err := writeRegexPrefix(dst, window, processLimit, re, repl)
			if err != nil {
				return matches, err
			}
			if len(window)-consumed > carryBudget {
				return matches, ErrRegexMatchExceededWindow
			}
			matches += int64(count)

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
			_, count, err := writeRegexPrefix(dst, carry, len(carry), re, repl)
			if err != nil {
				return matches, err
			}
			matches += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			return matches, dst.Sync()
		}
		if readErr != nil {
			return matches, readErr
		}
	}
}

// ReplaceRegexpFile streams sourcePath into outputPath through an exclusive temp file.
func ReplaceRegexpFile(ctx context.Context, sourcePath string, outputPath string, pattern []byte, repl []byte, opts FileOptions, regexOpts RegexOptions) (FileSummary, error) {
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
		Operation:     "regex-replace",
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
	matches, replaceErr := ReplaceRegexp(ctx, src, dst, pattern, repl, regexOpts)
	manifest.Matches = matches
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
	return summary, nil
}

// PreviewRegexp returns bounded before/after snippets for the first regex matches.
func PreviewRegexp(ctx context.Context, r ReaderAtSize, pattern []byte, repl []byte, opts RegexPreviewOptions, regexOpts RegexOptions) ([]Preview, error) {
	re, err := compileRegex(pattern, regexOpts.CaseInsensitive)
	if err != nil {
		return nil, err
	}

	regexOpts.ChunkSize = firstPositive(regexOpts.ChunkSize, opts.ChunkSize)
	results, err := collectRegexpMatches(ctx, r, re, regexOpts, opts.MaxHits)
	if err != nil {
		return nil, err
	}

	radius := opts.PreviewBytes
	if radius <= 0 {
		radius = 48
	}

	previews := make([]Preview, 0, len(results))
	for _, result := range results {
		before, after, err := regexReplacementSnippet(r, re, result.Offset, result.Length, repl, radius)
		if err != nil {
			return nil, err
		}
		previews = append(previews, Preview{
			Offset: result.Offset,
			Before: before,
			After:  after,
		})
	}
	return previews, nil
}

type regexMatch struct {
	Offset int64
	Length int
}

func collectRegexpMatches(ctx context.Context, r ReaderAtSize, re *regexp.Regexp, opts RegexOptions, maxHits int) ([]regexMatch, error) {
	opts = normalizeRegexOptions(opts)
	results := make([]regexMatch, 0, maxHits)
	buf := make([]byte, opts.ChunkSize)
	carryBudget := opts.MaxMatchWindow * 2
	carry := make([]byte, 0, carryBudget)
	window := make([]byte, 0, opts.ChunkSize+carryBudget)
	startOffset := int64(0)
	processed := int64(0)

	for processed < r.Size() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		want := opts.ChunkSize
		if remaining := r.Size() - processed; remaining < int64(want) {
			want = int(remaining)
		}

		n, readErr := r.ReadAt(buf[:want], processed)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
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

		consumed, err := collectRegexPrefix(window, processLimit, re, startOffset-int64(len(carry)), func(offset int64, length int) bool {
			results = append(results, regexMatch{Offset: offset, Length: length})
			return maxHits > 0 && len(results) >= maxHits
		})
		if err != nil {
			return nil, err
		}
		if len(window)-consumed > carryBudget {
			return nil, ErrRegexMatchExceededWindow
		}
		if maxHits > 0 && len(results) >= maxHits {
			return results, nil
		}

		carry = append(carry[:0], window[consumed:]...)
		processed += int64(n)
		startOffset = processed

		if errors.Is(readErr, io.EOF) && len(carry) > 0 {
			windowStart := startOffset - int64(len(carry))
			consumed, err := collectRegexPrefix(carry, len(carry), re, windowStart, func(offset int64, length int) bool {
				results = append(results, regexMatch{Offset: offset, Length: length})
				return maxHits > 0 && len(results) >= maxHits
			})
			if err != nil {
				return nil, err
			}
			if len(carry)-consumed > carryBudget {
				return nil, ErrRegexMatchExceededWindow
			}
			carry = carry[:0]
			if maxHits > 0 && len(results) >= maxHits {
				return results, nil
			}
		}
	}

	if len(carry) > 0 {
		windowStart := startOffset - int64(len(carry))
		_, err := collectRegexPrefix(carry, len(carry), re, windowStart, func(offset int64, length int) bool {
			results = append(results, regexMatch{Offset: offset, Length: length})
			return maxHits > 0 && len(results) >= maxHits
		})
		if err != nil {
			return nil, err
		}
	}

	return results, nil
}

// collectRegexPrefix iterates matches with a moving cursor (FindSubmatchIndex)
// instead of materializing every match up front (FindAllSubmatchIndex), so peak
// memory stays bounded by the window rather than the match count on dense inputs.
func collectRegexPrefix(window []byte, processLimit int, re *regexp.Regexp, windowStart int64, emit func(offset int64, length int) bool) (int, error) {
	consumed := processLimit
	for pos := 0; pos <= len(window); {
		loc := re.FindSubmatchIndex(window[pos:])
		if loc == nil {
			break
		}
		start, end := loc[0]+pos, loc[1]+pos
		if end > processLimit {
			if start < processLimit {
				consumed = start
			}
			break
		}
		if emit(windowStart+int64(start), end-start) {
			return consumed, nil
		}
		if loc[1] > loc[0] {
			pos = end
		} else {
			pos = end + 1 // zero-width match: advance to avoid an infinite loop
		}
	}
	return consumed, nil
}

// writeRegexPrefix streams matches one at a time with a moving cursor and reuses
// a single Expand scratch buffer, so a dense replace (e.g. a frequent token in a
// 16 MiB window) no longer allocates the full match-index slice plus a fresh
// expansion per match — keeping per-chunk allocation bounded by the window size.
func writeRegexPrefix(dst io.Writer, window []byte, processLimit int, re *regexp.Regexp, repl []byte) (consumed int, count int, err error) {
	cursor := 0
	consumed = processLimit
	var scratch []byte
	for pos := 0; pos <= len(window); {
		loc := re.FindSubmatchIndex(window[pos:])
		if loc == nil {
			break
		}
		start, end := loc[0]+pos, loc[1]+pos
		if end > processLimit {
			safeEnd := processLimit
			if start < safeEnd {
				safeEnd = start
			}
			if safeEnd > cursor {
				if _, err := dst.Write(window[cursor:safeEnd]); err != nil {
					return 0, count, err
				}
			}
			if start < processLimit {
				consumed = start
			}
			return consumed, count, nil
		}
		if _, err := dst.Write(window[cursor:start]); err != nil {
			return 0, count, err
		}
		scratch = re.Expand(scratch[:0], repl, window[pos:], loc)
		if _, err := dst.Write(scratch); err != nil {
			return 0, count, err
		}
		cursor = end
		count++
		if loc[1] > loc[0] {
			pos = end
		} else {
			pos = end + 1 // zero-width match: advance to avoid an infinite loop
		}
	}
	if cursor < processLimit {
		if _, err := dst.Write(window[cursor:processLimit]); err != nil {
			return 0, count, err
		}
	}
	return consumed, count, nil
}

func regexReplacementSnippet(r ReaderAtSize, re *regexp.Regexp, offset int64, length int, repl []byte, radius int) (string, string, error) {
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
		return "", "", errors.New("preview window does not contain regex match")
	}

	matchBytes := buf[localStart:localEnd]
	loc := re.FindSubmatchIndex(matchBytes)
	if loc == nil || loc[0] != 0 || loc[1] != len(matchBytes) {
		return "", "", errors.New("preview window could not re-expand regex match")
	}

	var after bytes.Buffer
	after.Write(buf[:localStart])
	after.Write(re.Expand(nil, repl, matchBytes, loc))
	after.Write(buf[localEnd:])

	return cleanSnippet(buf), cleanSnippet(after.Bytes()), nil
}

func compileRegex(pattern []byte, caseInsensitive bool) (*regexp.Regexp, error) {
	return regexutil.Compile(pattern, caseInsensitive)
}

func normalizeRegexOptions(opts RegexOptions) RegexOptions {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 16 * 1024 * 1024
	}
	if opts.MaxMatchWindow <= 0 {
		opts.MaxMatchWindow = defaultRegexMatchWindow
	}
	if opts.MaxMatchWindow > opts.ChunkSize {
		opts.MaxMatchWindow = opts.ChunkSize
	}
	return opts
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
