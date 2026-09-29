package csv

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/maphash"
	"strings"
)

const (
	// DefaultDedupeMaxDistinctKeys is the normal exact in-memory refusal
	// threshold. Callers may raise it only up to MaxDedupeDistinctKeys.
	DefaultDedupeMaxDistinctKeys = 250_000
	// MaxDedupeDistinctKeys is the application hard ceiling for retained exact
	// keys. Larger jobs require a future securely owned disk-backed strategy.
	MaxDedupeDistinctKeys = 1_000_000

	// DefaultDedupeMaxMemoryBytes is the conservative retained-memory budget for
	// the exact key set, including its fixed hash table.
	DefaultDedupeMaxMemoryBytes int64 = 128 * 1024 * 1024
	// MaxDedupeMemoryBytes is the application hard ceiling for the exact key set.
	MaxDedupeMemoryBytes int64 = 256 * 1024 * 1024

	// Accounting deliberately exceeds the current 64-bit slot footprint and
	// reserves allocator/runtime overhead.
	dedupeAccountedSlotBytes   int64 = 32
	dedupeAccountedSetBytes    int64 = 256
	dedupeAccountedKeyOverhead int64 = 64
)

// ErrDedupeBudgetExceeded marks a fail-closed exact-set refusal.
var ErrDedupeBudgetExceeded = errors.New("exact CSV deduplication budget exceeded")

type DedupeBudgetKind string

const (
	DedupeBudgetDistinctKeys DedupeBudgetKind = "distinct-keys"
	DedupeBudgetMemoryBytes  DedupeBudgetKind = "memory-bytes"
)

// DedupeBudgetError reports the deterministic refusal before the exact seen
// set grows beyond its configured limit.
type DedupeBudgetError struct {
	Kind         DedupeBudgetKind
	Record       int64
	Limit        int64
	Used         int64
	Required     int64
	DistinctKeys int
}

func (e *DedupeBudgetError) Error() string {
	unit := "bytes of retained memory"
	if e.Kind == DedupeBudgetDistinctKeys {
		unit = "distinct keys"
	}
	return fmt.Sprintf(
		"exact CSV deduplication refused record %d: %s would grow from %d to %d, exceeding the configured limit %d after %d distinct keys; disk-backed deduplication is not available and the final output was not published",
		e.Record, unit, e.Used, e.Required, e.Limit, e.DistinctKeys,
	)
}

func (e *DedupeBudgetError) Unwrap() error { return ErrDedupeBudgetExceeded }

const (
	dedupeKeyPresent byte = 1
	dedupeKeyMissing byte = 2
	dedupeKeyWhole   byte = 3
)

// dedupeKey canonically length-frames every selected field. Embedded NUL and
// all other bytes remain unambiguous, while marker bytes distinguish a present
// key cell, a missing key cell, and whole-row mode.
func dedupeKey(rec []string, keyColumn int) string {
	key, _ := dedupeKeyContext(context.Background(), rec, keyColumn)
	return key
}

func dedupeKeyContext(ctx context.Context, rec []string, keyColumn int) (string, error) {
	marker := dedupeKeyWhole
	fields := rec
	if keyColumn >= 0 {
		if keyColumn < len(rec) {
			marker = dedupeKeyPresent
			fields = rec[keyColumn : keyColumn+1]
		} else {
			marker = dedupeKeyMissing
			// Preserve distinct ragged rows when the configured key cell is
			// absent. Treating every missing cell as one empty key would silently
			// discard unrelated malformed/short records.
			fields = rec
		}
	}

	encodedBytes := 1 + uvarintBytes(uint64(len(fields)))
	for i, field := range fields {
		if i%4096 == 0 {
			if err := contextErr(ctx); err != nil {
				return "", err
			}
		}
		encodedBytes += uvarintBytes(uint64(len(field))) + len(field)
	}

	var builder strings.Builder
	builder.Grow(encodedBytes)
	builder.WriteByte(marker)
	writeDedupeUvarint(&builder, uint64(len(fields)))
	for i, field := range fields {
		if i%4096 == 0 {
			if err := contextErr(ctx); err != nil {
				return "", err
			}
		}
		writeDedupeUvarint(&builder, uint64(len(field)))
		builder.WriteString(field)
	}
	return builder.String(), nil
}

func uvarintBytes(value uint64) int {
	bytes := 1
	for value >= 0x80 {
		value >>= 7
		bytes++
	}
	return bytes
}

func writeDedupeUvarint(builder *strings.Builder, value uint64) {
	var encoded [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(encoded[:], value)
	_, _ = builder.Write(encoded[:n])
}

type dedupeSlot struct {
	hash uint64
	key  string
}

type dedupeSet struct {
	seed            maphash.Seed
	slots           []dedupeSlot
	maxDistinctKeys int
	maxMemoryBytes  int64
	distinctKeys    int
	memoryBytes     int64
}

func newDedupeSet(maxDistinctKeys int, maxMemoryBytes int64) (*dedupeSet, error) {
	if maxDistinctKeys <= 0 {
		return nil, fmt.Errorf("dedupe distinct-key limit must be positive, got %d", maxDistinctKeys)
	}
	if maxDistinctKeys > MaxDedupeDistinctKeys {
		return nil, fmt.Errorf("dedupe distinct-key limit %d exceeds application maximum %d", maxDistinctKeys, MaxDedupeDistinctKeys)
	}
	if maxMemoryBytes <= 0 {
		return nil, fmt.Errorf("dedupe memory limit must be positive, got %d", maxMemoryBytes)
	}
	if maxMemoryBytes > MaxDedupeMemoryBytes {
		return nil, fmt.Errorf("dedupe memory limit %d exceeds application maximum %d", maxMemoryBytes, MaxDedupeMemoryBytes)
	}
	baseBytes := dedupeSetBaseMemoryBytes(maxDistinctKeys)
	if baseBytes > maxMemoryBytes {
		return nil, fmt.Errorf(
			"dedupe memory limit %d cannot hold the %d-byte exact-set base allocation required for %d distinct keys; lower the key limit or raise the memory limit",
			maxMemoryBytes, baseBytes, maxDistinctKeys,
		)
	}
	return &dedupeSet{
		seed:            maphash.MakeSeed(),
		slots:           make([]dedupeSlot, dedupeTableCapacity(maxDistinctKeys)),
		maxDistinctKeys: maxDistinctKeys,
		maxMemoryBytes:  maxMemoryBytes,
		memoryBytes:     baseBytes,
	}, nil
}

// add compares complete canonical keys after the keyed hash, so a hash
// collision can never collapse distinct rows.
func (s *dedupeSet) add(key string, record int64) (bool, error) {
	if key == "" {
		return false, errors.New("canonical dedupe key is empty")
	}
	return s.addHashed(key, maphash.String(s.seed, key), record)
}

func (s *dedupeSet) addHashed(key string, hash uint64, record int64) (bool, error) {
	if key == "" {
		return false, errors.New("canonical dedupe key is empty")
	}
	mask := len(s.slots) - 1
	index := int(hash & uint64(mask))
	for probes := 0; probes < len(s.slots); probes++ {
		slot := &s.slots[index]
		if slot.key == "" {
			if s.distinctKeys >= s.maxDistinctKeys {
				return false, &DedupeBudgetError{
					Kind: DedupeBudgetDistinctKeys, Record: record,
					Limit: int64(s.maxDistinctKeys), Used: int64(s.distinctKeys), Required: int64(s.distinctKeys + 1),
					DistinctKeys: s.distinctKeys,
				}
			}
			keyBytes := dedupeKeyMemoryBytes(len(key))
			required := s.memoryBytes + keyBytes
			if required > s.maxMemoryBytes {
				return false, &DedupeBudgetError{
					Kind: DedupeBudgetMemoryBytes, Record: record,
					Limit: s.maxMemoryBytes, Used: s.memoryBytes, Required: required,
					DistinctKeys: s.distinctKeys,
				}
			}
			slot.hash = hash
			slot.key = key
			s.distinctKeys++
			s.memoryBytes = required
			return true, nil
		}
		if slot.hash == hash && slot.key == key {
			return false, nil
		}
		index = (index + 1) & mask
	}
	return false, errors.New("exact dedupe set has no free slot before its configured key limit")
}

func dedupeTableCapacity(maxDistinctKeys int) int {
	required := maxDistinctKeys * 2
	capacity := 1
	for capacity < required {
		capacity <<= 1
	}
	return capacity
}

func dedupeSetBaseMemoryBytes(maxDistinctKeys int) int64 {
	return dedupeAccountedSetBytes + int64(dedupeTableCapacity(maxDistinctKeys))*dedupeAccountedSlotBytes
}

func dedupeKeyMemoryBytes(encodedBytes int) int64 {
	bytes := int64(encodedBytes)
	return bytes + (bytes+3)/4 + dedupeAccountedKeyOverhead
}

func normalizeDedupeOptions(opts DedupeOptions) (DedupeOptions, error) {
	if err := ValidateDelimiter(opts.Delimiter); err != nil {
		return opts, err
	}
	limit, err := normalizeLogicalRecordLimit(opts.MaxRecordBytes)
	if err != nil {
		return opts, err
	}
	opts.MaxRecordBytes = limit

	if opts.MaxDistinctKeys < 0 {
		return opts, fmt.Errorf("dedupe distinct-key limit %d is negative", opts.MaxDistinctKeys)
	}
	if opts.MaxDistinctKeys == 0 {
		opts.MaxDistinctKeys = DefaultDedupeMaxDistinctKeys
	}
	if opts.MaxDistinctKeys > MaxDedupeDistinctKeys {
		return opts, fmt.Errorf("dedupe distinct-key limit %d exceeds application maximum %d", opts.MaxDistinctKeys, MaxDedupeDistinctKeys)
	}

	if opts.MaxMemoryBytes < 0 {
		return opts, fmt.Errorf("dedupe memory limit %d is negative", opts.MaxMemoryBytes)
	}
	if opts.MaxMemoryBytes == 0 {
		opts.MaxMemoryBytes = DefaultDedupeMaxMemoryBytes
	}
	if opts.MaxMemoryBytes > MaxDedupeMemoryBytes {
		return opts, fmt.Errorf("dedupe memory limit %d exceeds application maximum %d", opts.MaxMemoryBytes, MaxDedupeMemoryBytes)
	}
	if baseBytes := dedupeSetBaseMemoryBytes(opts.MaxDistinctKeys); baseBytes > opts.MaxMemoryBytes {
		return opts, fmt.Errorf(
			"dedupe memory limit %d cannot hold the %d-byte exact-set base allocation required for %d distinct keys; lower the key limit or raise the memory limit",
			opts.MaxMemoryBytes, baseBytes, opts.MaxDistinctKeys,
		)
	}
	return opts, nil
}

// ValidateDedupeOptions validates allocation-driving configuration before the
// source or atomic output is opened.
func ValidateDedupeOptions(opts DedupeOptions) error {
	_, err := normalizeDedupeOptions(opts)
	return err
}
