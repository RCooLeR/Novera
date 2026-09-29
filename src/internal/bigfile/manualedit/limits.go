package manualedit

import (
	"errors"
	"fmt"
	"math"
)

const (
	DefaultMaxLiveInsertedBytes int64 = 16 * 1024 * 1024
	DefaultMaxHistoryBytes      int64 = 48 * 1024 * 1024
	DefaultMaxTransientBytes    int64 = 192 * 1024 * 1024
	DefaultMaxEditCount               = 4096
	DefaultMaxHistoryDepth            = 4096
	DefaultMaxPieceCount              = 8193

	// These intentionally overestimate the current Go structure sizes. They
	// are safety-accounting constants, not promises about compiler layout.
	pieceAccountingBytes   int64 = 32
	rangeAccountingBytes   int64 = 24
	historyAccountingBytes int64 = 96
	sliceAccountingBytes   int64 = 24
	sourceWriteBufferBytes int64 = 1024 * 1024
)

var (
	ErrEditCountLimit           = errors.New("active edit count exceeds the session limit")
	ErrHistoryDepthLimit        = errors.New("retained undo history exceeds the session depth limit")
	ErrHistoryBytesLimit        = errors.New("retained undo history exceeds the session memory limit")
	ErrTransientMemoryLimit     = errors.New("edit rebuild would exceed the transient memory limit")
	ErrInvalidSessionLimits     = errors.New("manual-edit session limits are invalid")
	ErrSessionRevisionExhausted = errors.New("manual-edit session revision is exhausted")
)

// Limits caps every cumulative in-memory dimension of one manual-edit
// session. Zero is invalid: callers overriding a field should start with
// DefaultLimits so a partially initialized policy cannot remove a bound.
type Limits struct {
	MaxEditTextBytes     int64
	MaxLiveInsertedBytes int64
	MaxHistoryBytes      int64
	MaxTransientBytes    int64
	MaxEditCount         int
	MaxHistoryDepth      int
	MaxPieceCount        int
}

func DefaultLimits() Limits {
	return Limits{
		MaxEditTextBytes:     DefaultMaxInsertedBytes,
		MaxLiveInsertedBytes: DefaultMaxLiveInsertedBytes,
		MaxHistoryBytes:      DefaultMaxHistoryBytes,
		MaxTransientBytes:    DefaultMaxTransientBytes,
		MaxEditCount:         DefaultMaxEditCount,
		MaxHistoryDepth:      DefaultMaxHistoryDepth,
		MaxPieceCount:        DefaultMaxPieceCount,
	}
}

func validateLimits(limits Limits) error {
	if limits.MaxEditTextBytes <= 0 ||
		limits.MaxLiveInsertedBytes <= 0 ||
		limits.MaxHistoryBytes <= 0 ||
		limits.MaxTransientBytes <= 0 ||
		limits.MaxEditCount <= 0 ||
		limits.MaxHistoryDepth <= 0 ||
		limits.MaxPieceCount <= 0 {
		return ErrInvalidSessionLimits
	}
	if limits.MaxEditTextBytes > int64(maxInt()) ||
		limits.MaxLiveInsertedBytes > int64(maxInt()) ||
		limits.MaxHistoryBytes > int64(maxInt()) ||
		limits.MaxTransientBytes > int64(maxInt()) {
		return fmt.Errorf("%w: byte limit exceeds addressable memory", ErrInvalidSessionLimits)
	}
	return nil
}

func legacyLimits(maxInsertedBytes int64) Limits {
	limits := DefaultLimits()
	if maxInsertedBytes > 0 {
		limits.MaxEditTextBytes = maxInsertedBytes
		limits.MaxLiveInsertedBytes = maxInsertedBytes
		// Preserve the legacy constructor's small custom limit as a meaningful
		// total-memory policy while keeping every dimension explicitly bounded.
		if maxInsertedBytes <= math.MaxInt64/4 {
			candidate := maxInsertedBytes * 4
			if candidate > historyAccountingBytes {
				limits.MaxHistoryBytes = candidate
				if candidate <= math.MaxInt64/2 {
					limits.MaxTransientBytes = candidate * 2
				}
			}
		}
	}
	return limits
}

func checkedMemorySum(values ...int64) (int64, error) {
	var total int64
	for _, value := range values {
		if value < 0 || value > math.MaxInt64-total {
			return 0, ErrTransientMemoryLimit
		}
		total += value
	}
	return total, nil
}

func checkedMemoryProduct(value int64, multiplier int64) (int64, error) {
	if value < 0 || multiplier < 0 || (value != 0 && multiplier > math.MaxInt64/value) {
		return 0, ErrTransientMemoryLimit
	}
	return value * multiplier, nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
