package analyze

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type panicReader struct{}

func (panicReader) Size() int64 { panic("source must not be inspected") }

func (panicReader) ReadAt([]byte, int64) (int, error) {
	panic("source must not be read")
}

type observedReader struct {
	data      []byte
	sizeCalls int
	readCalls int
	onRead    func()
}

func (r *observedReader) Size() int64 {
	r.sizeCalls++
	return int64(len(r.data))
}

func (r *observedReader) ReadAt(p []byte, off int64) (int, error) {
	r.readCalls++
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if r.onRead != nil {
		r.onRead()
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func assertLimitError(t *testing.T, summary Summary, err error, kind LimitKind, limit int) {
	t.Helper()
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("error = %v, want ErrLimitExceeded", err)
	}
	var limitErr *LimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("error type = %T, want *LimitError", err)
	}
	if limitErr.Kind != kind || limitErr.Limit != limit || limitErr.Observed <= limit {
		t.Fatalf("limit error = %+v, want kind=%q limit=%d observed>%d", limitErr, kind, limit, limit)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Fatalf("limit failure returned partial summary: %#v", summary)
	}
}

func TestAnalyzeRejectsOversizedChunkBeforeSourceAccess(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	summary, err := Analyze(context.Background(), panicReader{}, Options{ChunkSize: maxInt})
	assertLimitError(t, summary, err, LimitChunkBytes, MaxChunkBytes)
}

func TestAnalyzeRejectsInvalidOptionsBeforeSourceAccess(t *testing.T) {
	summary, err := Analyze(context.Background(), panicReader{}, Options{ChunkSize: -1})
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("error = %v, want ErrInvalidOptions", err)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Fatalf("invalid option returned partial summary: %#v", summary)
	}
}

func TestAnalyzeFileRejectsOversizedChunkBeforeOpeningPath(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	summary, err := AnalyzeFile(context.Background(), t.TempDir()+"/missing.sql", Options{ChunkSize: maxInt})
	assertLimitError(t, summary, err, LimitChunkBytes, MaxChunkBytes)
}

func TestAnalyzeRejectsTableCardinalityWithoutPartialSummary(t *testing.T) {
	var sql strings.Builder
	sql.Grow((MaxTableCount + 1) * 32)
	for i := 0; i <= MaxTableCount; i++ {
		sql.WriteString("INSERT INTO t")
		sql.WriteString(strconv.Itoa(i))
		sql.WriteString(" VALUES (1);\n")
	}

	summary, err := Analyze(context.Background(), memReader{data: []byte(sql.String())}, Options{ChunkSize: 64 * 1024})
	assertLimitError(t, summary, err, LimitTables, MaxTableCount)
}

func TestAnalyzeRejectsStatsNameCardinalityWithoutPartialSummary(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		count  int
		kind   LimitKind
		limit  int
	}{
		{name: "charsets", prefix: "CHARSET=cs", count: MaxDistinctCharsetNames + 1, kind: LimitCharsetNames, limit: MaxDistinctCharsetNames},
		{name: "collations", prefix: "COLLATE=co", count: MaxDistinctCollationNames + 1, kind: LimitCollationNames, limit: MaxDistinctCollationNames},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sql strings.Builder
			for i := 0; i < tc.count; i++ {
				sql.WriteString(tc.prefix)
				sql.WriteString(strconv.Itoa(i))
				sql.WriteByte(' ')
			}
			summary, err := Analyze(context.Background(), memReader{data: []byte(sql.String())}, Options{ChunkSize: 32 * 1024})
			assertLimitError(t, summary, err, tc.kind, tc.limit)
		})
	}
}

func TestAnalyzeRejectsOverlongMetadataIdentifiers(t *testing.T) {
	longName := strings.Repeat("x", MaxIdentifierBytes+1)
	tests := []struct {
		name string
		sql  string
	}{
		{name: "create table", sql: "CREATE TABLE `" + longName + "` (id int);"},
		{name: "create whitespace table", sql: "CREATE TABLE `" + strings.Repeat(" ", MaxIdentifierBytes+1) + "` (id int);"},
		{name: "create database qualifier", sql: "CREATE TABLE `" + longName + "`.`t` (id int);"},
		{name: "insert table", sql: "INSERT INTO " + longName + " VALUES (1);"},
		{name: "insert database qualifier", sql: "INSERT INTO `" + longName + "`.`t` VALUES (1);"},
		{name: "charset", sql: "CREATE TABLE t (id int) CHARSET=" + longName + ";"},
		{name: "collation", sql: "CREATE TABLE t (id int) COLLATE=" + longName + ";"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summary, err := Analyze(context.Background(), memReader{data: []byte(tc.sql)}, Options{ChunkSize: 256})
			assertLimitError(t, summary, err, LimitIdentifierBytes, MaxIdentifierBytes)
		})
	}
}

func TestAnalyzeAcceptsIdentifierAtHardLimit(t *testing.T) {
	name := strings.Repeat("x", MaxIdentifierBytes)
	text := "CREATE TABLE `" + name + "` (id int); INSERT INTO `" + name + "` VALUES (1);"
	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != name || summary.CreateTables != 1 || summary.InsertTables != 1 {
		t.Fatalf("summary at identifier limit = %#v", summary)
	}
}

func TestAnalyzeDenseCreateAndInsertMatches(t *testing.T) {
	const matchCount = 20_000
	const statement = "CREATE DEFINER=`root`@`localhost` TABLE IF NOT EXISTS `repeat` (id int) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin; INSERT INTO `repeat` VALUES (1);\n"
	text := strings.Repeat(statement, matchCount)

	summary, err := Analyze(context.Background(), memReader{data: []byte(text)}, Options{ChunkSize: 4 * 1024 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tables) != 1 || summary.Tables[0].Name != "repeat" || summary.CreateTables != 1 || summary.InsertTables != 1 {
		t.Fatalf("dense table summary = %#v", summary)
	}
	if summary.DefinerCount != matchCount || summary.Charsets["utf8mb4"] != matchCount || summary.Collations["utf8mb4_bin"] != matchCount {
		t.Fatalf("dense counters = definers:%d charsets:%#v collations:%#v", summary.DefinerCount, summary.Charsets, summary.Collations)
	}
}

func TestAnalyzeCancellationReturnsNoPartialSummary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &observedReader{
		data:   []byte("CREATE TABLE before_cancel (id int);"),
		onRead: cancel,
	}
	summary, err := Analyze(ctx, reader, Options{ChunkSize: 16})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if reader.sizeCalls != 1 || reader.readCalls != 1 {
		t.Fatalf("source calls = size:%d read:%d, want 1/1", reader.sizeCalls, reader.readCalls)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Fatalf("cancellation returned partial summary: %#v", summary)
	}
}

func TestAnalyzePreCancelledContextDoesNotInspectSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summary, err := Analyze(ctx, panicReader{}, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if !reflect.DeepEqual(summary, Summary{}) {
		t.Fatalf("pre-cancelled analysis returned partial summary: %#v", summary)
	}
}
