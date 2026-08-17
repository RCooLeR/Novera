package csv

import (
	"bytes"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

type boundedSampleStepReader struct {
	data      []byte
	step      int
	zeroReads int
	calls     int
	maxRead   int
}

func (r *boundedSampleStepReader) Read(p []byte) (int, error) {
	r.calls++
	if len(p) > r.maxRead {
		r.maxRead = len(p)
	}
	if r.zeroReads > 0 {
		r.zeroReads--
		return 0, nil
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	step := r.step
	if step <= 0 || step > len(p) {
		step = len(p)
	}
	if step > len(r.data) {
		step = len(r.data)
	}
	copy(p, r.data[:step])
	r.data = r.data[step:]
	return step, nil
}

func TestReadBoundedSampleResultExactTruncationAndBOMSemantics(t *testing.T) {
	tests := []struct {
		name      string
		input     []byte
		maxBytes  int64
		want      []byte
		truncated bool
	}{
		{name: "short source", input: []byte("ab"), maxBytes: 4, want: []byte("ab")},
		{name: "exact source", input: []byte("abcd"), maxBytes: 4, want: []byte("abcd")},
		{name: "one byte over", input: []byte("abcde"), maxBytes: 4, want: []byte("abcd"), truncated: true},
		{name: "UTF-8 BOM exact payload", input: append(append([]byte{}, utf8BOM...), []byte("abcd")...), maxBytes: 4, want: []byte("abcd")},
		{name: "UTF-8 BOM over payload", input: append(append([]byte{}, utf8BOM...), []byte("abcde")...), maxBytes: 4, want: []byte("abcd"), truncated: true},
		{name: "partial UTF-8 BOM is data", input: []byte{0xEF, 0xBB}, maxBytes: 2, want: []byte{0xEF, 0xBB}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &boundedSampleStepReader{data: append([]byte(nil), test.input...), step: 1}
			sample, err := readBoundedSampleResult(context.Background(), reader, test.maxBytes)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sample.Data, test.want) {
				t.Fatalf("data = % X, want % X", sample.Data, test.want)
			}
			if sample.BytesScanned != int64(len(test.want)) {
				t.Fatalf("bytes scanned = %d, want %d", sample.BytesScanned, len(test.want))
			}
			if sample.Truncated != test.truncated {
				t.Fatalf("truncated = %t, want %t", sample.Truncated, test.truncated)
			}
			if cap(sample.Data) != len(sample.Data) {
				t.Fatalf("returned sample exposes look-ahead capacity: len=%d cap=%d", len(sample.Data), cap(sample.Data))
			}
		})
	}
}

func TestReadBoundedSampleResultRejectsInvalidLimitsBeforeRead(t *testing.T) {
	for _, limit := range []int64{-1, MaxSampleBytes + 1, math.MaxInt64} {
		reader := &boundedSampleStepReader{data: []byte("must not be read")}
		if _, err := readBoundedSampleResult(context.Background(), reader, limit); !errors.Is(err, ErrCSVSampleLimit) {
			t.Fatalf("limit %d error = %v, want ErrCSVSampleLimit", limit, err)
		}
		if reader.calls != 0 {
			t.Fatalf("limit %d performed %d reads before validation", limit, reader.calls)
		}
	}

	reader := &boundedSampleStepReader{}
	sample, err := readBoundedSampleResult(context.Background(), reader, MaxSampleBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.Data) != 0 || reader.maxRead > len(utf8BOM) {
		t.Fatalf("empty hard-limit sample = len %d, max read request %d", len(sample.Data), reader.maxRead)
	}
}

func TestBoundedSampleAPIsRejectInvalidLimitsBeforeRead(t *testing.T) {
	type callAPI func(context.Context, io.Reader, int64, int) error
	apis := []struct {
		name string
		call callAPI
	}{
		{"inspect", func(ctx context.Context, r io.Reader, maxBytes int64, maxRows int) error {
			_, err := InspectReaderContext(ctx, r, InspectOptions{MaxBytes: maxBytes, MaxRows: maxRows})
			return err
		}},
		{"preview", func(ctx context.Context, r io.Reader, maxBytes int64, maxRows int) error {
			_, err := PreviewRowsContext(ctx, r, PreviewOptions{MaxBytes: maxBytes, MaxRows: maxRows})
			return err
		}},
		{"schema", func(ctx context.Context, r io.Reader, maxBytes int64, maxRows int) error {
			_, err := InferSchemaContext(ctx, r, SchemaOptions{MaxBytes: maxBytes, MaxRows: maxRows})
			return err
		}},
		{"profile", func(ctx context.Context, r io.Reader, maxBytes int64, maxRows int) error {
			_, err := ProfileColumns(ctx, r, SchemaOptions{MaxBytes: maxBytes, MaxRows: maxRows})
			return err
		}},
		{"column guide", func(ctx context.Context, r io.Reader, maxBytes int64, maxRows int) error {
			_, err := BuildColumnGuideContext(ctx, r, ColumnGuideOptions{MaxBytes: maxBytes, MaxRows: maxRows})
			return err
		}},
		{"project preview", func(ctx context.Context, r io.Reader, maxBytes int64, maxRows int) error {
			_, err := PreviewProjectedColumnsContext(ctx, r, ProjectPreviewOptions{Columns: []int{0}, MaxBytes: maxBytes, MaxRows: maxRows})
			return err
		}},
	}
	limits := []struct {
		name     string
		maxBytes int64
		maxRows  int
	}{
		{name: "negative bytes", maxBytes: -1},
		{name: "bytes over hard limit", maxBytes: MaxSampleBytes + 1},
		{name: "negative rows", maxRows: -1},
		{name: "rows over hard limit", maxRows: MaxSampleRows + 1},
	}

	for _, api := range apis {
		for _, limit := range limits {
			t.Run(api.name+"/"+limit.name, func(t *testing.T) {
				reader := &boundedSampleStepReader{data: []byte("must not be read")}
				err := api.call(context.Background(), reader, limit.maxBytes, limit.maxRows)
				if !errors.Is(err, ErrCSVSampleLimit) {
					t.Fatalf("error = %v, want ErrCSVSampleLimit", err)
				}
				if reader.calls != 0 {
					t.Fatalf("invalid options performed %d reads", reader.calls)
				}
			})
		}
	}
}

func TestCompleteRecordPrefixIgnoresPhysicalNewlineInsidePartialQuotedRecord(t *testing.T) {
	data := []byte("a,b\n1,\"line\npartial")
	prefix, omitted, err := CompleteRecordPrefix(context.Background(), data, ',', 1024, true)
	if err != nil {
		t.Fatal(err)
	}
	if !omitted {
		t.Fatal("expected partial logical record to be omitted")
	}
	if got := string(prefix); got != "a,b\n" {
		t.Fatalf("complete prefix = %q, want only header record", got)
	}
}

func TestCompleteRecordPrefixRejectsBareQuoteAtTruncatedBoundary(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		cause error
	}{
		{name: "bare quote", data: "a,b\n1,ba\"", cause: stdcsv.ErrBareQuote},
		{name: "extraneous quote suffix", data: "a,b\n1,\"a\"x", cause: stdcsv.ErrQuote},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := CompleteRecordPrefix(context.Background(), []byte(test.data), ',', 1024, true)
			if !errors.Is(err, test.cause) {
				t.Fatalf("error = %v, want %v", err, test.cause)
			}
		})
	}
}

func TestReadBoundedSampleResultStopsAfterRepeatedEmptyReads(t *testing.T) {
	stalled := &boundedSampleStepReader{zeroReads: maxConsecutiveEmptyReads + 1}
	if _, err := readBoundedSampleResult(context.Background(), stalled, 32); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("stalled reader error = %v, want io.ErrNoProgress", err)
	}
}

func TestPreviewRowsOmitsTruncatedMultilineRecord(t *testing.T) {
	input := "a,b\n1,complete\n2,\"partial\nstill partial\"\n"
	cut := int64(strings.Index(input, "still partial") + 5)
	report, err := PreviewRowsContext(context.Background(), strings.NewReader(input), PreviewOptions{
		Delimiter: ',', HasHeader: true, MaxBytes: cut, MaxRows: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.TruncatedSample {
		t.Fatal("expected truncated sample")
	}
	if len(report.Rows) != 1 || report.Rows[0][0] != "1" {
		t.Fatalf("preview rows = %#v, want only complete row", report.Rows)
	}
}
