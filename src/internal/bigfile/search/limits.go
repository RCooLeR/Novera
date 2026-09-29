package search

import (
	"errors"
	"fmt"
)

const (
	// MaxChunkBytes bounds every caller-selectable search read buffer.
	MaxChunkBytes = 64 * 1024 * 1024
	// MaxPlainPatternBytes bounds literal copies, folded copies, and seam carry.
	MaxPlainPatternBytes = 1024 * 1024
	// MaxCollectedHits is the absolute result-count ceiling for APIs that return
	// an in-memory slice. Streaming Find APIs intentionally do not use this cap.
	MaxCollectedHits = 10_000
	// MaxPreviewBytes is the largest retained context radius on either side of
	// one materialized search result.
	MaxPreviewBytes = 64 * 1024
	// MaxCollectedResultBytes is a conservative retained-result budget. It is
	// checked before scanning with overflow-free division.
	MaxCollectedResultBytes int64 = 64 * 1024 * 1024

	resultAccountingBytes int64 = 128
)

var ErrResourceLimit = errors.New("search resource limit exceeded")

func searchLimit(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrResourceLimit, fmt.Sprintf(format, args...))
}

func validateStreamingOptions(chunkSize int, maxHits int) error {
	if chunkSize < 0 {
		return searchLimit("chunk size must not be negative")
	}
	if chunkSize > MaxChunkBytes {
		return searchLimit("chunk size %d exceeds %d bytes", chunkSize, MaxChunkBytes)
	}
	if maxHits < 0 {
		return searchLimit("maximum hits must not be negative")
	}
	return nil
}

func validatePlainPattern(pattern []byte) error {
	if len(pattern) == 0 {
		return errors.New("empty pattern")
	}
	if len(pattern) > MaxPlainPatternBytes {
		return searchLimit("plain pattern has %d bytes, maximum is %d", len(pattern), MaxPlainPatternBytes)
	}
	return nil
}

func validateCollectRequest(patternBytes int, maxHits int, previewBytes int) error {
	if maxHits <= 0 {
		return searchLimit("materialized search requires a positive maximum hit count")
	}
	if maxHits > MaxCollectedHits {
		return searchLimit("maximum hits %d exceeds %d", maxHits, MaxCollectedHits)
	}
	if previewBytes < 0 {
		return searchLimit("preview bytes must not be negative")
	}
	if previewBytes > MaxPreviewBytes {
		return searchLimit("preview radius %d exceeds %d bytes", previewBytes, MaxPreviewBytes)
	}

	// One result retains its match bytes plus both preview sides and slice/string
	// metadata. Use division so MaxInt hostile values cannot overflow a product.
	perResult := int64(patternBytes) + 2*int64(previewBytes) + resultAccountingBytes
	if perResult <= 0 || int64(maxHits) > MaxCollectedResultBytes/perResult {
		return searchLimit("requested hits and previews exceed the %d-byte result budget", MaxCollectedResultBytes)
	}
	return nil
}
