package csv

import (
	"context"
	stdcsv "encoding/csv"
	"errors"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type seamReader struct {
	data []byte
	step int
}

func (r *seamReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.step, len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestBoundedCSVReaderPreservesMultilineEscapedQuotesAcrossSeams(t *testing.T) {
	input := "id,note\r\n1,\"line one\nline \"\"two\"\"\"\r\n2,  padded\r\n"
	for seam := 1; seam <= 7; seam++ {
		t.Run(string(rune('0'+seam)), func(t *testing.T) {
			reader, err := newBoundedCSVReader(context.Background(), &seamReader{data: []byte(input), step: seam}, csvReaderConfig{
				Delimiter: ',', MaxRecordBytes: int64(len(input)), FieldsPerRecord: -1,
			})
			if err != nil {
				t.Fatal(err)
			}
			records, err := reader.ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 3 || records[1][1] != "line one\nline \"two\"" || records[2][1] != "  padded" {
				t.Fatalf("records = %#v", records)
			}
		})
	}
}

func TestBoundedCSVReaderPreservesTrimLeadingSpaceMultilineAcrossSeams(t *testing.T) {
	input := "  \"alpha\nbeta\",  tail\r\n"
	for seam := 1; seam <= 7; seam++ {
		reader, err := newBoundedCSVReader(context.Background(), &seamReader{data: []byte(input), step: seam}, csvReaderConfig{
			Delimiter: ',', MaxRecordBytes: int64(len(input)), FieldsPerRecord: -1,
			TrimLeadingSpace: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		record, err := reader.Read()
		if err != nil {
			t.Fatalf("seam %d: %v", seam, err)
		}
		if len(record) != 2 || record[0] != "alpha\nbeta" || record[1] != "tail" {
			t.Fatalf("seam %d record = %#v", seam, record)
		}
	}
}

func TestBoundedCSVReaderPreservesStrictAndLazyQuoteSemantics(t *testing.T) {
	const input = "a\"b,c\n"
	strict, err := newBoundedCSVReader(context.Background(), strings.NewReader(input), csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: int64(len(input)), FieldsPerRecord: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict.Read(); err == nil {
		t.Fatal("strict reader accepted a bare quote")
	}

	lazy, err := newBoundedCSVReader(context.Background(), strings.NewReader(input), csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: int64(len(input)), FieldsPerRecord: -1, LazyQuotes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := lazy.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(record) != 2 || record[0] != `a"b` || record[1] != "c" {
		t.Fatalf("lazy record = %#v", record)
	}
}

func TestBoundedCSVReaderAcceptsExactLimitAndRejectsOneByteOver(t *testing.T) {
	record := "\"alpha\nbeta\",tail\n"
	reader, err := newBoundedCSVReader(context.Background(), strings.NewReader(record), csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: int64(len(record)), FieldsPerRecord: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.Read()
	if err != nil || len(got) != 2 || got[0] != "alpha\nbeta" {
		t.Fatalf("exact-limit record = %#v, %v", got, err)
	}

	reader, err = newBoundedCSVReader(context.Background(), strings.NewReader(record), csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: int64(len(record) - 1), FieldsPerRecord: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Read()
	assertRecordLimitError(t, err, 1, 0, int64(len(record)-1))
}

func TestBoundedCSVReaderAcceptsExactFieldLimitAndRejectsNextField(t *testing.T) {
	exact := strings.TrimSuffix(strings.Repeat("x,", MaxCSVFieldsPerRecord), ",") + "\n"
	reader, err := newBoundedCSVReader(context.Background(), &seamReader{data: []byte(exact), step: 3}, csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: int64(len(exact)), FieldsPerRecord: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(record) != MaxCSVFieldsPerRecord {
		t.Fatalf("field count = %d, want %d", len(record), MaxCSVFieldsPerRecord)
	}

	over := strings.Repeat(",", MaxCSVFieldsPerRecord) + "\n"
	reader, err = newBoundedCSVReader(context.Background(), &seamReader{data: []byte(over), step: 1}, csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: int64(len(over)), FieldsPerRecord: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Read()
	assertFieldLimitError(t, err, 1, 0, MaxCSVFieldsPerRecord+1)
}

func TestBoundedCSVReaderFieldLimitIgnoresQuotedDelimitersAndReportsSecondRecord(t *testing.T) {
	quoted := "\"" + strings.Repeat(",", MaxCSVFieldsPerRecord+17) + "\",tail\n"
	reader, err := newBoundedCSVReader(context.Background(), &seamReader{data: []byte(quoted), step: 2}, csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: int64(len(quoted)), FieldsPerRecord: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := reader.Read()
	if err != nil || len(record) != 2 || record[0] != strings.Repeat(",", MaxCSVFieldsPerRecord+17) {
		t.Fatalf("quoted record = %#v, %v", record, err)
	}

	input := "ok\n" + strings.Repeat(",", MaxCSVFieldsPerRecord) + "\n"
	reader, err = newBoundedCSVReader(context.Background(), strings.NewReader(input), csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: int64(len(input)), FieldsPerRecord: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first, err := reader.Read(); err != nil || !reflect.DeepEqual(first, []string{"ok"}) {
		t.Fatalf("first record = %#v, %v", first, err)
	}
	_, err = reader.Read()
	assertFieldLimitError(t, err, 2, 3, MaxCSVFieldsPerRecord+1)
}

func TestBoundedCSVReaderRejectsHugeFieldAndReportsSecondRecordOffset(t *testing.T) {
	input := "ok\n" + strings.Repeat("x", 65) + "\n"
	reader, err := newBoundedCSVReader(context.Background(), strings.NewReader(input), csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: 64, FieldsPerRecord: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := reader.Read()
	if err != nil || len(first) != 1 || first[0] != "ok" {
		t.Fatalf("first record = %#v, %v", first, err)
	}
	_, err = reader.Read()
	assertRecordLimitError(t, err, 2, 3, 64)
}

func TestBoundedCSVReaderRejectsNewlineDenseUnterminatedQuote(t *testing.T) {
	input := "\"" + strings.Repeat("x\n", 80)
	reader, err := newBoundedCSVReader(context.Background(), &seamReader{data: []byte(input), step: 3}, csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: 96, FieldsPerRecord: -1, LazyQuotes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Read()
	assertRecordLimitError(t, err, 1, 0, 96)
}

func TestBoundedCSVReaderStopsBeforeReadingWholeOversizedRecord(t *testing.T) {
	source := &byteCountingReader{data: []byte("\"" + strings.Repeat("payload", 150_000))}
	sourceBytes := len(source.data)
	reader, err := newBoundedCSVReader(context.Background(), source, csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: 1024, FieldsPerRecord: -1, LazyQuotes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Read()
	assertRecordLimitError(t, err, 1, 0, 1024)
	if source.read >= sourceBytes {
		t.Fatalf("reader consumed the whole %d-byte source before rejecting it", sourceBytes)
	}
	if source.read > 32*1024 {
		t.Fatalf("reader prefetched %d bytes, want at most its 32 KiB source buffer", source.read)
	}
}

func TestBoundedCSVReaderCancellationDuringLogicalRecord(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &cancelAfterReader{
		data:   []byte("\"" + strings.Repeat("payload\n", 32*1024)),
		cancel: cancel,
		after:  8 * 1024,
	}
	reader, err := newBoundedCSVReader(ctx, source, csvReaderConfig{
		Delimiter: ',', MaxRecordBytes: MaxLogicalRecordBytes, FieldsPerRecord: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Read()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestBoundedCSVReaderRejectsInvalidLimitBeforeReading(t *testing.T) {
	source := &countReadsReader{reader: strings.NewReader("a,b\n")}
	for _, limit := range []int64{-1, MaxLogicalRecordBytes + 1} {
		if _, err := newBoundedCSVReader(context.Background(), source, csvReaderConfig{Delimiter: ',', MaxRecordBytes: limit}); err == nil {
			t.Fatalf("limit %d was accepted", limit)
		}
		if source.reads != 0 {
			t.Fatalf("limit %d read source %d times before validation", limit, source.reads)
		}
	}
}

func TestBoundedCSVReaderConcurrentInstances(t *testing.T) {
	const workers = 16
	input := "id,note\n1,\"alpha\nbeta\"\n2,\"escaped \"\"quote\"\"\"\n"
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(step int) {
			defer wg.Done()
			reader, err := newBoundedCSVReader(context.Background(), &seamReader{data: []byte(input), step: step}, csvReaderConfig{
				Delimiter: ',', MaxRecordBytes: 64, FieldsPerRecord: -1,
			})
			if err != nil {
				errs <- err
				return
			}
			records, err := reader.ReadAll()
			if err != nil {
				errs <- err
				return
			}
			if len(records) != 3 || records[1][1] != "alpha\nbeta" || records[2][1] != `escaped "quote"` {
				errs <- errors.New("concurrent reader returned different records")
			}
		}(worker%7 + 1)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestBoundedCSVReaderMatchesEncodingCSVBelowLimit(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	alphabet := []byte{'a', 'b', ',', '"', '\n', '\r', ' ', '\t', 0xff}
	for _, lazyQuotes := range []bool{false, true} {
		for _, trimLeadingSpace := range []bool{false, true} {
			for sample := 0; sample < 5000; sample++ {
				input := make([]byte, random.Intn(65))
				for i := range input {
					input[i] = alphabet[random.Intn(len(alphabet))]
				}

				direct := stdcsv.NewReader(strings.NewReader(string(input)))
				direct.FieldsPerRecord = -1
				direct.LazyQuotes = lazyQuotes
				direct.TrimLeadingSpace = trimLeadingSpace
				want, wantErr := direct.ReadAll()

				bounded, err := newBoundedCSVReader(context.Background(), &seamReader{
					data: append([]byte(nil), input...), step: sample%7 + 1,
				}, csvReaderConfig{
					Delimiter: ',', MaxRecordBytes: int64(len(input) + 1), FieldsPerRecord: -1,
					LazyQuotes: lazyQuotes, TrimLeadingSpace: trimLeadingSpace,
				})
				if err != nil {
					t.Fatal(err)
				}
				got, gotErr := bounded.ReadAll()
				if (gotErr == nil) != (wantErr == nil) {
					t.Fatalf("lazy=%t trim=%t input=%q: bounded error=%v, encoding/csv error=%v", lazyQuotes, trimLeadingSpace, input, gotErr, wantErr)
				}
				if wantErr == nil && !reflect.DeepEqual(got, want) {
					t.Fatalf("lazy=%t trim=%t input=%q: bounded=%#v, encoding/csv=%#v", lazyQuotes, trimLeadingSpace, input, got, want)
				}
			}
		}
	}
}

type cancelAfterReader struct {
	data   []byte
	read   int
	after  int
	cancel context.CancelFunc
}

type byteCountingReader struct {
	data []byte
	read int
}

func (r *byteCountingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	r.read += n
	return n, nil
}

func (r *cancelAfterReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), 1024, len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	r.read += n
	if r.read >= r.after {
		r.cancel()
	}
	return n, nil
}

type countReadsReader struct {
	reader io.Reader
	reads  int
}

func (r *countReadsReader) Read(p []byte) (int, error) {
	r.reads++
	return r.reader.Read(p)
}

func assertRecordLimitError(t *testing.T, err error, record, offset, limit int64) {
	t.Helper()
	if !errors.Is(err, ErrCSVRecordTooLarge) {
		t.Fatalf("error = %v, want ErrCSVRecordTooLarge", err)
	}
	var limitErr *RecordLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("error type = %T, want *RecordLimitError", err)
	}
	if limitErr.Record != record || limitErr.StartOffset != offset || limitErr.LimitBytes != limit || limitErr.ObservedBytes <= limit {
		t.Fatalf("limit error = %+v", limitErr)
	}
}

func assertFieldLimitError(t *testing.T, err error, record, offset int64, observed int) {
	t.Helper()
	if !errors.Is(err, ErrCSVTooManyFields) {
		t.Fatalf("error = %v, want ErrCSVTooManyFields", err)
	}
	var limitErr *FieldLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("error type = %T, want *FieldLimitError", err)
	}
	if limitErr.Record != record || limitErr.StartOffset != offset || limitErr.LimitFields != MaxCSVFieldsPerRecord || limitErr.ObservedFields != observed {
		t.Fatalf("field limit error = %+v", limitErr)
	}
}
