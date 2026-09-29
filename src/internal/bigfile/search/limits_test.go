package search

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type limitReader struct {
	size int64
}

func (r limitReader) Size() int64 { return r.size }
func (limitReader) ReadAt([]byte, int64) (int, error) {
	panic("resource-limit rejection must happen before a source read")
}

func TestCollectPlainRejectsUnboundedAndHostileLimits(t *testing.T) {
	tests := []struct {
		name    string
		opts    PlainOptions
		preview int
	}{
		{name: "zero hits", opts: PlainOptions{ChunkSize: 1, MaxHits: 0}},
		{name: "negative hits", opts: PlainOptions{ChunkSize: 1, MaxHits: -1}},
		{name: "excessive hits", opts: PlainOptions{ChunkSize: 1, MaxHits: MaxCollectedHits + 1}},
		{name: "maximum integer hits", opts: PlainOptions{ChunkSize: 1, MaxHits: int(^uint(0) >> 1)}},
		{name: "negative preview", opts: PlainOptions{ChunkSize: 1, MaxHits: 1}, preview: -1},
		{name: "excessive preview", opts: PlainOptions{ChunkSize: 1, MaxHits: 1}, preview: MaxPreviewBytes + 1},
		{name: "negative chunk", opts: PlainOptions{ChunkSize: -1, MaxHits: 1}},
		{name: "excessive chunk", opts: PlainOptions{ChunkSize: MaxChunkBytes + 1, MaxHits: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CollectPlain(context.Background(), limitReader{size: 1}, []byte("x"), tt.opts, tt.preview)
			if !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("error = %v, want ErrResourceLimit", err)
			}
		})
	}
}

func TestCollectRegexpRejectsUnboundedCollectorBeforeReading(t *testing.T) {
	for _, hits := range []int{-1, 0, MaxCollectedHits + 1, int(^uint(0) >> 1)} {
		_, err := CollectRegexp(context.Background(), limitReader{size: 1}, []byte("x"), RegexOptions{ChunkSize: 1, MaxHits: hits, MaxMatchWindow: 1}, 0)
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("MaxHits=%d error = %v, want ErrResourceLimit", hits, err)
		}
	}
}

func TestCollectRegexpAccountsForMaximumMatchWidth(t *testing.T) {
	pattern := []byte(strings.Repeat("a{1000}", 64)) // 64,000-byte maximum match
	_, err := CollectRegexp(context.Background(), limitReader{size: 1}, pattern, RegexOptions{
		ChunkSize:      64 * 1024,
		MaxHits:        2_000,
		MaxMatchWindow: 64 * 1024,
	}, 0)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("error = %v, want ErrResourceLimit", err)
	}
}

func TestCollectLimitsAcceptIndividualMaximumBoundaries(t *testing.T) {
	if err := validateStreamingOptions(MaxChunkBytes, 0); err != nil {
		t.Fatalf("maximum chunk rejected: %v", err)
	}
	if err := validatePlainPattern(make([]byte, MaxPlainPatternBytes)); err != nil {
		t.Fatalf("maximum pattern rejected: %v", err)
	}
	if err := validateCollectRequest(1, MaxCollectedHits, 0); err != nil {
		t.Fatalf("maximum hits rejected: %v", err)
	}
	if err := validateCollectRequest(1, 1, MaxPreviewBytes); err != nil {
		t.Fatalf("maximum preview rejected: %v", err)
	}
}

func TestCollectLimitsRejectOversizedPatternAndAggregateWithoutReading(t *testing.T) {
	pattern := make([]byte, MaxPlainPatternBytes+1)
	if _, err := CollectPlain(context.Background(), limitReader{size: 1}, pattern, PlainOptions{ChunkSize: 1, MaxHits: 1}, 0); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("pattern error = %v, want ErrResourceLimit", err)
	}

	pattern = make([]byte, MaxPlainPatternBytes)
	if _, err := CollectPlain(context.Background(), limitReader{size: 1}, pattern, PlainOptions{ChunkSize: 1, MaxHits: 100}, 0); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("aggregate error = %v, want ErrResourceLimit", err)
	}
}

func TestStreamingPlainSearchStillAllowsExplicitUnboundedCallback(t *testing.T) {
	r := memReaderAt{data: []byte{}}
	if err := FindPlain(context.Background(), r, []byte("x"), PlainOptions{ChunkSize: 1, MaxHits: 0}, func(Match) error { return io.ErrUnexpectedEOF }); err != nil {
		t.Fatalf("streaming search rejected zero MaxHits: %v", err)
	}
}
