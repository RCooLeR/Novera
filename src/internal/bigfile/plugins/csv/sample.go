package csv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	// MaxSampleBytes is the application hard ceiling for CSV payload retained in
	// a bounded in-memory sample. A stripped UTF-8 BOM and the truncation
	// look-ahead byte are accounted for separately with checked arithmetic.
	MaxSampleBytes int64 = 64 * 1024 * 1024
	// MaxSampleRows bounds caller-controlled row loops over an in-memory sample.
	MaxSampleRows = 200_000

	sampleReadChunkBytes     = int64(32 * 1024)
	sampleInitialCapacity    = int64(512)
	maxConsecutiveEmptyReads = 100
)

var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}
)

// ErrUTF16Input is returned when input looks like UTF-16. The CSV tools operate
// on UTF-8 and don't transcode, so this gives the user an actionable message
// instead of a confusing "control character U+0000" failure on the NUL bytes.
var ErrUTF16Input = errors.New("input looks like UTF-16; re-save the file as UTF-8")

// ErrCSVSampleLimit identifies invalid byte and row limits for bounded sample
// APIs. Callers can use errors.Is without depending on the descriptive text.
var ErrCSVSampleLimit = errors.New("invalid CSV sample limit")

// detectUTF16BOM reports whether b begins with a UTF-16 byte-order mark.
func detectUTF16BOM(b []byte) bool {
	return bytes.HasPrefix(b, utf16LEBOM) || bytes.HasPrefix(b, utf16BEBOM)
}

// skipInputBOM peeks a streaming reader: it errors on a UTF-16 BOM and discards
// a UTF-8 BOM so it isn't counted or fed to the CSV parser.
func skipInputBOM(br *bufio.Reader) error {
	peek, _ := br.Peek(3)
	if detectUTF16BOM(peek) {
		return ErrUTF16Input
	}
	if len(peek) >= len(utf8BOM) && bytes.Equal(peek[:len(utf8BOM)], utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	return nil
}

type boundedSample struct {
	Data         []byte
	BytesScanned int64
	Truncated    bool
}

// readBoundedSample preserves the historical maxBytes+1 return contract for
// call sites that have not yet migrated to boundedSample metadata.
func readBoundedSample(ctx context.Context, r io.Reader, maxBytes int64) ([]byte, error) {
	sample, err := readBoundedSampleResult(ctx, r, maxBytes)
	if err != nil {
		return nil, err
	}
	if !sample.Truncated {
		return sample.Data, nil
	}
	data := make([]byte, len(sample.Data)+1)
	copy(data, sample.Data)
	return data, nil
}

func normalizeSampleByteLimit(name string, requested, defaultValue int64) (int64, error) {
	if requested < 0 {
		return 0, fmt.Errorf("%s byte limit %d is negative: %w", name, requested, ErrCSVSampleLimit)
	}
	if requested == 0 {
		requested = defaultValue
	}
	if requested <= 0 {
		return 0, fmt.Errorf("%s byte limit must be positive: %w", name, ErrCSVSampleLimit)
	}
	if requested > MaxSampleBytes {
		return 0, fmt.Errorf("%s byte limit %d exceeds application maximum %d: %w", name, requested, MaxSampleBytes, ErrCSVSampleLimit)
	}
	return requested, nil
}

func normalizeSampleRowLimit(name string, requested, defaultValue int) (int, error) {
	if requested < 0 {
		return 0, fmt.Errorf("%s row limit %d is negative: %w", name, requested, ErrCSVSampleLimit)
	}
	if requested == 0 {
		requested = defaultValue
	}
	if requested <= 0 {
		return 0, fmt.Errorf("%s row limit must be positive: %w", name, ErrCSVSampleLimit)
	}
	if requested > MaxSampleRows {
		return 0, fmt.Errorf("%s row limit %d exceeds application maximum %d: %w", name, requested, MaxSampleRows, ErrCSVSampleLimit)
	}
	return requested, nil
}

func readBoundedSampleResult(ctx context.Context, r io.Reader, maxBytes int64) (boundedSample, error) {
	if r == nil {
		return boundedSample{}, errors.New("reader is required")
	}
	if maxBytes <= 0 || maxBytes > MaxSampleBytes {
		return boundedSample{}, fmt.Errorf("sample byte limit %d is outside 1..%d: %w", maxBytes, MaxSampleBytes, ErrCSVSampleLimit)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return boundedSample{}, err
	}

	// A Reader has no general cancellation contract. Run the bounded loop in a
	// worker when the context is cancellable so a blocked Read cannot pin the
	// caller. The worker owns all buffers; after cancellation it exits as soon as
	// the in-flight Read returns, without racing a returned buffer.
	if ctx.Done() == nil {
		return readBoundedSampleLoop(ctx, r, maxBytes)
	}
	type result struct {
		sample boundedSample
		err    error
	}
	done := make(chan result, 1)
	go func() {
		sample, err := readBoundedSampleLoop(ctx, r, maxBytes)
		done <- result{sample: sample, err: err}
	}()

	select {
	case <-ctx.Done():
		return boundedSample{}, ctx.Err()
	case result := <-done:
		if err := ctx.Err(); err != nil {
			return boundedSample{}, err
		}
		return result.sample, result.err
	}
}

func readBoundedSampleLoop(ctx context.Context, r io.Reader, maxBytes int64) (boundedSample, error) {
	// At most three BOM bytes plus maxBytes of payload plus one look-ahead byte
	// are retained. maxBytes has already been capped, but keep the addition
	// explicitly checked so this helper remains safe if the policy changes.
	overhead := int64(len(utf8BOM)) + 1
	if maxBytes > math.MaxInt64-overhead {
		return boundedSample{}, fmt.Errorf("sample byte limit %d overflows look-ahead arithmetic: %w", maxBytes, ErrCSVSampleLimit)
	}
	maxRetained := maxBytes + overhead
	initialCapacity := minInt64(maxRetained, sampleInitialCapacity)
	raw := make([]byte, 0, int(initialCapacity))
	scratch := make([]byte, int(minInt64(maxRetained, sampleReadChunkBytes)))

	// Probe exactly the longest supported BOM first. Once the prefix is known,
	// the target becomes payload limit + one byte, plus any stripped UTF-8 BOM.
	prefixBytes := int64(-1)
	target := int64(len(utf8BOM))
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return boundedSample{}, err
		}
		if prefixBytes >= 0 {
			target = prefixBytes + maxBytes + 1
		}
		if int64(len(raw)) >= target {
			if prefixBytes >= 0 {
				break
			}
			prefixBytes = 0
			if bytes.HasPrefix(raw, utf8BOM) {
				prefixBytes = int64(len(utf8BOM))
			}
			continue
		}

		remaining := target - int64(len(raw))
		readSize := minInt64(remaining, sampleReadChunkBytes)
		chunk := scratch[:int(readSize)]
		n, err := r.Read(chunk)
		if n < 0 || n > len(chunk) {
			return boundedSample{}, fmt.Errorf("reader returned invalid byte count %d for a %d-byte buffer", n, len(chunk))
		}
		if n > 0 {
			raw = appendWithBoundedCapacity(raw, chunk[:n], int(maxRetained))
			emptyReads = 0
		} else if err == nil {
			emptyReads++
			if emptyReads >= maxConsecutiveEmptyReads {
				return boundedSample{}, io.ErrNoProgress
			}
		}

		if detectUTF16BOM(raw) {
			return boundedSample{}, ErrUTF16Input
		}
		if prefixBytes < 0 && (len(raw) >= len(utf8BOM) || errors.Is(err, io.EOF)) {
			prefixBytes = 0
			if bytes.HasPrefix(raw, utf8BOM) {
				prefixBytes = int64(len(utf8BOM))
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return boundedSample{}, err
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}

	if prefixBytes < 0 {
		prefixBytes = 0
	}
	payload := raw[int(prefixBytes):]
	truncated := int64(len(payload)) > maxBytes
	if truncated {
		payload = payload[:int(maxBytes)]
	}
	// Hide the look-ahead byte from callers and prevent accidental append from
	// exposing it through spare capacity.
	payload = payload[:len(payload):len(payload)]
	return boundedSample{
		Data:         payload,
		BytesScanned: int64(len(payload)),
		Truncated:    truncated,
	}, nil
}

func appendWithBoundedCapacity(dst, src []byte, maxCapacity int) []byte {
	required := len(dst) + len(src)
	if required <= cap(dst) {
		return append(dst, src...)
	}
	capacity := cap(dst) * 2
	if capacity < required {
		capacity = required
	}
	if capacity > maxCapacity {
		capacity = maxCapacity
	}
	grown := make([]byte, len(dst), capacity)
	copy(grown, dst)
	return append(grown, src...)
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
