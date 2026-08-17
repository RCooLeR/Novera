package replace

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func runSQLPlainReplace(t *testing.T, source, find, replacement string, options BatchOptions) (string, int64, error) {
	t.Helper()
	var output bytes.Buffer
	matches, err := ReplaceSQLPlain(
		context.Background(),
		strings.NewReader(source),
		&output,
		int64(len(source)),
		[]byte(find),
		[]byte(replacement),
		options,
	)
	return output.String(), matches, err
}

func TestSQLPlainReplaceRecountsPHPSerializedLengths(t *testing.T) {
	source := "INSERT INTO wp_options VALUES ('a:2:{s:3:\"url\";s:15:\"http://old.test\";s:5:\"emoji\";s:4:\"\xf0\x9f\x98\x80\";}');\n"
	want := "INSERT INTO wp_options VALUES ('a:2:{s:3:\"url\";s:18:\"http://new.example\";s:5:\"emoji\";s:4:\"\xf0\x9f\x98\x80\";}');\n"

	got, matches, err := runSQLPlainReplace(t, source, "old.test", "new.example", BatchOptions{ChunkSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Fatalf("matches = %d, want 1", matches)
	}
	if got != want {
		t.Fatalf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestSQLPlainReplacePreservesQuotedTextInCommentsAndIdentifiers(t *testing.T) {
	source := "-- 'http://old.test' stays\n" +
		"# 'http://old.test' also stays\n" +
		"/* outer 'http://old.test' /* nested 'http://old.test' */ still comment */\n" +
		"CREATE TABLE `old.test` ([old.test] text, \"old.test\" text);\n" +
		"INSERT INTO `old.test` VALUES ('http://old.test');\n"
	want := strings.Replace(source, "VALUES ('http://old.test')", "VALUES ('http://new.example')", 1)

	got, matches, err := runSQLPlainReplace(t, source, "old.test", "new.example", BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Fatalf("matches = %d, want 1", matches)
	}
	if got != want {
		t.Fatalf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestSQLPlainReplaceCopiesUntouchedLiteralByteForByte(t *testing.T) {
	source := "INSERT INTO t VALUES ('keep\\\\path\\nverbatim', 'http://old.test', X'6f6c642e74657374');\n"
	want := "INSERT INTO t VALUES ('keep\\\\path\\nverbatim', 'http://new.example', X'6f6c642e74657374');\n"

	got, matches, err := runSQLPlainReplace(t, source, "old.test", "new.example", BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Fatalf("matches = %d, want 1", matches)
	}
	if got != want {
		t.Fatalf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestSQLPlainReplaceRejectsChangedBackslashEscapedLiteral(t *testing.T) {
	source := "INSERT INTO t VALUES ('prefix\\\\http://old.test');\n"
	_, _, err := runSQLPlainReplace(t, source, "old.test", "new.example", BatchOptions{})
	if !errors.Is(err, ErrUnsupportedSQLReplaceContext) {
		t.Fatalf("error = %v, want ErrUnsupportedSQLReplaceContext", err)
	}
}

func TestSQLPlainReplaceRejectsMalformedAndOverflowingSerializedLengths(t *testing.T) {
	tests := []string{
		"INSERT INTO t VALUES ('s:99:\"http://old.test\";');\n",
		"INSERT INTO t VALUES ('s:999999999999999999999999999999999999:\"http://old.test\";');\n",
		"INSERT INTO t VALUES ('a:999999999999999999999999999999999999:{s:15:\"http://old.test\";}');\n",
		"INSERT INTO t VALUES ('O:999999999999999999999999999999999999:\"old.test\":0:{}');\n",
	}
	for _, source := range tests {
		_, _, err := runSQLPlainReplace(t, source, "old.test", "new.example", BatchOptions{})
		if !errors.Is(err, ErrUnsupportedPHPSerializedData) {
			t.Fatalf("source %q: error = %v, want ErrUnsupportedPHPSerializedData", source, err)
		}
	}
}

func TestSQLPlainReplaceRejectsExcessiveSerializedDepth(t *testing.T) {
	value := `s:15:"http://old.test";`
	for index := 0; index < maxPHPSerializedDepth+2; index++ {
		value = "a:1:{i:0;" + value + "}"
	}
	source := "INSERT INTO t VALUES ('" + value + "');\n"

	_, _, err := runSQLPlainReplace(t, source, "old.test", "new.example", BatchOptions{})
	if !errors.Is(err, ErrUnsupportedPHPSerializedData) {
		t.Fatalf("error = %v, want ErrUnsupportedPHPSerializedData", err)
	}
}

func TestSQLPlainReplaceRejectsOpaqueCustomSerializedPayload(t *testing.T) {
	source := "INSERT INTO t VALUES ('C:4:\"Demo\":15:{http://old.test}');\n"
	_, _, err := runSQLPlainReplace(t, source, "old.test", "new.example", BatchOptions{})
	if !errors.Is(err, ErrUnsupportedPHPSerializedData) {
		t.Fatalf("error = %v, want ErrUnsupportedPHPSerializedData", err)
	}
}

func TestSQLPlainReplaceBoundsDenseExpansionBeforeAllocation(t *testing.T) {
	replacement := strings.Repeat("x", MaxSQLReplaceReplacementBytes)
	source := "INSERT INTO t VALUES ('aaaaa');\n"
	_, _, err := runSQLPlainReplace(t, source, "a", replacement, BatchOptions{})
	if !errors.Is(err, ErrSQLReplaceResourceLimit) {
		t.Fatalf("error = %v, want ErrSQLReplaceResourceLimit", err)
	}
}

func TestSQLPlainReplaceRejectsUnsupportedSQLContexts(t *testing.T) {
	tests := []string{
		"DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 'http://old.test'; END$$\n",
		"SET sql_mode='NO_BACKSLASH_ESCAPES';\nINSERT INTO t VALUES ('http://old.test');\n",
		"COPY imported(value) FROM STDIN;\nhttp://old.test\n\\.\n",
		"DO $body$ BEGIN RAISE NOTICE 'http://old.test'; END $body$;\n",
		"INSERT INTO t VALUES (E'http://old.test');\n",
	}
	for _, source := range tests {
		_, _, err := runSQLPlainReplace(t, source, "old.test", "new.example", BatchOptions{})
		if !errors.Is(err, ErrUnsupportedSQLReplaceContext) {
			t.Fatalf("source %q: error = %v, want ErrUnsupportedSQLReplaceContext", source, err)
		}
	}
}

func TestSQLPlainReplaceCaseInsensitiveWholeWordInsideSerialization(t *testing.T) {
	source := "INSERT INTO t VALUES ('a:2:{s:3:\"one\";s:15:\"OLD.TEST suffix\";s:3:\"two\";s:14:\"xOLD.TESTvalue\";}');\n"
	want := "INSERT INTO t VALUES ('a:2:{s:3:\"one\";s:18:\"new.example suffix\";s:3:\"two\";s:14:\"xOLD.TESTvalue\";}');\n"

	got, matches, err := runSQLPlainReplace(t, source, "old.test", "new.example", BatchOptions{CaseInsensitive: true, WholeWord: true})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Fatalf("matches = %d, want 1", matches)
	}
	if got != want {
		t.Fatalf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestSQLPlainReplaceRejectsEncodedSerializedPayloadWhenRawEncodingWouldChange(t *testing.T) {
	tests := []struct {
		source string
		find   string
	}{
		{"INSERT INTO t VALUES ('YToxOntzOjM6InVybCI7czoxNToiaHR0cDovL29sZC50ZXN0Ijt9');\n", "YTox"},
		{"INSERT INTO t VALUES ('613A313A7B733A333A2275726C223B733A31353A22687474703A2F2F6F6C642E74657374223B7D');\n", "613A"},
	}
	for _, test := range tests {
		_, _, err := runSQLPlainReplace(t, test.source, test.find, "changed", BatchOptions{})
		if !errors.Is(err, ErrUnsupportedPHPSerializedData) {
			t.Fatalf("error = %v, want ErrUnsupportedPHPSerializedData", err)
		}
	}
}

func TestSQLPlainReplaceHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	_, err := ReplaceSQLPlain(ctx, strings.NewReader("SELECT 'old';"), &output, 13, []byte("old"), []byte("new"), BatchOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if output.Len() != 0 {
		t.Fatalf("wrote %d bytes after pre-cancellation", output.Len())
	}
}
