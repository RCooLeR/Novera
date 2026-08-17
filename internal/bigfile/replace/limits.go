package replace

import (
	"errors"
	"fmt"

	"novera/internal/bigfile/regexutil"
)

const (
	// MaxPlainChunkBytes bounds caller-selected plain and batch working buffers.
	MaxPlainChunkBytes = 64 * 1024 * 1024
	// MaxPlainPatternBytes bounds literal carry, folded copies, and preview match
	// bytes. Batch patterns use a smaller package-specific limit.
	MaxPlainPatternBytes = 1024 * 1024
	// MaxPlainReplacementBytes bounds one caller-owned replacement copied into a
	// retained preview or regex expansion buffer.
	MaxPlainReplacementBytes = 16 * 1024 * 1024
	// MaxPreviewHits is the hard count ceiling for every slice-returning preview.
	MaxPreviewHits = 10_000
	// MaxPreviewBytes is the largest context radius retained on either side.
	MaxPreviewBytes = 64 * 1024
	// MaxPreviewRetainedBytes is a conservative package-level peak budget for
	// result snippets, collector rows, and capture locations.
	MaxPreviewRetainedBytes int64 = 64 * 1024 * 1024
	// MaxPreviewMatchBytes follows the exact regex-window ceiling.
	MaxPreviewMatchBytes = regexutil.MaxExactMatchWindowBytes
	// MaxRegexExpandedReplacementBytes bounds regexp.Expand scratch per match.
	MaxRegexExpandedReplacementBytes = 16 * 1024 * 1024

	defaultPreviewBytes    = 48
	previewAccountingBytes = 512
)

var ErrResourceLimit = errors.New("replace resource limit exceeded")

func replaceLimit(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrResourceLimit, fmt.Sprintf(format, args...))
}

func validatePlainTransformInputs(pattern []byte, replacement []byte, chunkSize int) error {
	if len(pattern) == 0 {
		return errors.New("empty pattern")
	}
	if len(pattern) > MaxPlainPatternBytes {
		return replaceLimit("plain pattern has %d bytes, maximum is %d", len(pattern), MaxPlainPatternBytes)
	}
	if len(replacement) > MaxPlainReplacementBytes {
		return replaceLimit("replacement has %d bytes, maximum is %d", len(replacement), MaxPlainReplacementBytes)
	}
	if chunkSize < 0 {
		return replaceLimit("chunk size must not be negative")
	}
	if chunkSize > MaxPlainChunkBytes {
		return replaceLimit("chunk size %d exceeds %d bytes", chunkSize, MaxPlainChunkBytes)
	}
	return nil
}

func normalizedPlainChunkSize(requested int, defaultSize int, minimum int) (int, error) {
	if requested < 0 {
		return 0, replaceLimit("chunk size must not be negative")
	}
	if requested == 0 {
		requested = defaultSize
	}
	if requested < minimum {
		requested = minimum
	}
	if requested > MaxPlainChunkBytes {
		return 0, replaceLimit("chunk size %d exceeds %d bytes", requested, MaxPlainChunkBytes)
	}
	return requested, nil
}

func validatePreviewBase(chunkSize int, maxHits int, previewBytes int) (int, error) {
	if chunkSize < 0 {
		return 0, replaceLimit("preview chunk size must not be negative")
	}
	if chunkSize > MaxPlainChunkBytes {
		return 0, replaceLimit("preview chunk size %d exceeds %d bytes", chunkSize, MaxPlainChunkBytes)
	}
	if maxHits <= 0 {
		return 0, replaceLimit("materialized preview requires a positive maximum hit count")
	}
	if maxHits > MaxPreviewHits {
		return 0, replaceLimit("maximum preview hits %d exceeds %d", maxHits, MaxPreviewHits)
	}
	if previewBytes < 0 {
		return 0, replaceLimit("preview bytes must not be negative")
	}
	if previewBytes == 0 {
		previewBytes = defaultPreviewBytes
	}
	if previewBytes > MaxPreviewBytes {
		return 0, replaceLimit("preview radius %d exceeds %d bytes", previewBytes, MaxPreviewBytes)
	}
	return previewBytes, nil
}

func validatePreviewBudget(maxHits int, radius int, matchBytes int, replacementBytes int, captureSlots int) error {
	if matchBytes < 0 || matchBytes > MaxPreviewMatchBytes {
		return replaceLimit("preview match has %d bytes, maximum is %d", matchBytes, MaxPreviewMatchBytes)
	}
	if replacementBytes < 0 || replacementBytes > MaxPlainReplacementBytes {
		return replaceLimit("preview replacement has %d bytes, maximum is %d", replacementBytes, MaxPlainReplacementBytes)
	}
	if captureSlots < 0 || captureSlots > 2*(MaxPlainPatternBytes+1) {
		return replaceLimit("preview capture-location count exceeds the safety limit")
	}

	perHit := int64(previewAccountingBytes) +
		2*int64(matchBytes) + int64(replacementBytes) +
		6*int64(radius) + 8*int64(captureSlots)
	if perHit <= 0 || int64(maxHits) > MaxPreviewRetainedBytes/perHit {
		return replaceLimit("requested previews exceed the %d-byte retained-result budget", MaxPreviewRetainedBytes)
	}
	return nil
}

func validateRegexReplacementExpansion(replacement []byte, maxMatchBytes int64) (int, error) {
	if len(replacement) > MaxPlainReplacementBytes {
		return 0, replaceLimit("replacement has %d bytes, maximum is %d", len(replacement), MaxPlainReplacementBytes)
	}
	if maxMatchBytes < 0 || maxMatchBytes > MaxPreviewMatchBytes {
		return 0, replaceLimit("regex maximum match has %d bytes, maximum is %d", maxMatchBytes, MaxPreviewMatchBytes)
	}

	references := 0
	for index := 0; index < len(replacement); index++ {
		if replacement[index] != '$' {
			continue
		}
		if index+1 < len(replacement) && replacement[index+1] == '$' {
			index++
			continue
		}
		references++
	}
	estimated := int64(len(replacement))
	if maxMatchBytes > 0 && int64(references) > (int64(MaxRegexExpandedReplacementBytes)-estimated)/maxMatchBytes {
		return 0, replaceLimit("expanded regex replacement exceeds %d bytes", MaxRegexExpandedReplacementBytes)
	}
	estimated += int64(references) * maxMatchBytes
	if estimated > MaxRegexExpandedReplacementBytes {
		return 0, replaceLimit("expanded regex replacement exceeds %d bytes", MaxRegexExpandedReplacementBytes)
	}
	return int(estimated), nil
}

func maxBatchPreviewDimensions(rules []BatchRule) (patternBytes int, replacementBytes int) {
	for _, rule := range rules {
		if len(rule.Find) > patternBytes {
			patternBytes = len(rule.Find)
		}
		if len(rule.Replace) > replacementBytes {
			replacementBytes = len(rule.Replace)
		}
	}
	return patternBytes, replacementBytes
}
