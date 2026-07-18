package datatools

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

const pgDump = "--\n-- PostgreSQL database dump\n--\n" +
	"CREATE TABLE public.users (id integer, name text, bio text);\n" +
	"COPY public.users (id, name, bio) FROM stdin;\n" +
	"1\tAda\tlikes \\t tabs\n" +
	"2\tBob\t\n" +
	"3\tO'Neill\tline\\nbreak\n" +
	"\\.\n" +
	"CREATE TABLE public.orders (id integer);\n" +
	"COPY public.orders (id) FROM stdin;\n" +
	"10\n" +
	"\\.\n"

type copyErrReader struct{ err error }

func (r copyErrReader) Read([]byte) (int, error) { return 0, r.err }

func TestAnalyzeDetectsCopy(t *testing.T) {
	sum, err := AnalyzeSQLDump(strings.NewReader(pgDump), 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.CopyBlocks != 2 {
		t.Errorf("copyBlocks = %d, want 2", sum.CopyBlocks)
	}
	if len(sum.Tables) != 2 {
		t.Fatalf("tables = %d, want 2", len(sum.Tables))
	}
	// Each table has both a CREATE and a COPY (InsertOffset) recorded.
	for _, tb := range sum.Tables {
		if tb.CreateOffset < 0 || tb.InsertOffset < 0 {
			t.Errorf("table %s missing offsets: %+v", tb.Name, tb)
		}
	}
}

func TestDumpTableToCSV(t *testing.T) {
	var out strings.Builder
	rows, found, err := DumpTableToCSV(strings.NewReader(pgDump), "users", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("users COPY block not found")
	}
	if rows != 3 {
		t.Fatalf("rows = %d, want 3", rows)
	}
	got := out.String()
	want := "id,name,bio\n" +
		"1,Ada,likes \t tabs\n" + // \t decoded to a real tab inside the (quoted by csv) field
		"2,Bob,\n" + // an actual empty field remains empty
		"3,O'Neill,\"line\nbreak\"\n" // embedded newline → csv-quoted
	if got != want {
		t.Fatalf("CSV mismatch:\n got %q\nwant %q", got, want)
	}

	// Match on the bare table name too.
	var o2 strings.Builder
	if _, found, _ := DumpTableToCSV(strings.NewReader(pgDump), "orders", &o2); !found {
		t.Error("orders (bare name) not matched")
	}
}

func TestDumpTableToCSVPreservesNullEmptyAndLiteralNullMarker(t *testing.T) {
	dump := "COPY public.values (kind, value) FROM stdin;\n" +
		"null\t\\N\n" +
		"empty\t\n" +
		"literal\t\\\\N\n" +
		"\\.\n"
	var out strings.Builder
	rows, found, err := DumpTableToCSV(strings.NewReader(dump), "public.values", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !found || rows != 3 {
		t.Fatalf("found=%v rows=%d, want true/3", found, rows)
	}
	if got, want := out.String(), "kind,value\nnull,\\N\nempty,\nliteral,\\\\N\n"; got != want {
		t.Fatalf("CSV mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestDumpTableToCSVQualifiedNameDoesNotMergeSchemas(t *testing.T) {
	dump := "COPY audit.users (id) FROM stdin;\n9\n\\.\n" +
		"COPY public.users (id) FROM stdin;\n1\n\\.\n"
	var out strings.Builder
	rows, found, err := DumpTableToCSV(strings.NewReader(dump), "public.users", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !found || rows != 1 {
		t.Fatalf("found=%v rows=%d, want true/1", found, rows)
	}
	if got, want := out.String(), "id\n1\n"; got != want {
		t.Fatalf("qualified extraction = %q, want %q", got, want)
	}
}

func TestDumpTableToCSVDoesNotParseNonTargetCopyDataAsSQL(t *testing.T) {
	dump := "COPY audit.log (payload) FROM stdin;\n" +
		"COPY public.users (id) FROM stdin;\n" +
		"999\n" +
		"\\.\n" +
		"COPY public.users (id) FROM stdin;\n" +
		"1\n" +
		"\\.\n"
	var out strings.Builder
	rows, found, err := DumpTableToCSV(strings.NewReader(dump), "public.users", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !found || rows != 1 {
		t.Fatalf("found=%v rows=%d, want true/1", found, rows)
	}
	if got, want := out.String(), "id\n1\n"; got != want {
		t.Fatalf("extraction = %q, want %q", got, want)
	}
}

func TestDumpTableToCSVRejectsAmbiguousBareName(t *testing.T) {
	dump := "COPY audit.users (id) FROM stdin;\n9\n\\.\n" +
		"COPY public.users (id) FROM stdin;\n1\n\\.\n"
	_, _, err := DumpTableToCSV(strings.NewReader(dump), "users", &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v, want ambiguous bare table refusal", err)
	}
}

func TestDumpTableToCSVRejectsTruncatedBlocks(t *testing.T) {
	tests := []struct {
		name string
		dump string
	}{
		{name: "copy", dump: "COPY public.users (id) FROM stdin;\n1\n"},
		{name: "insert", dump: "INSERT INTO public.users (id) VALUES\n(1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, found, err := DumpTableToCSV(strings.NewReader(tt.dump), "public.users", &strings.Builder{})
			if !found {
				t.Fatal("target table was not recognized")
			}
			if err == nil || !strings.Contains(err.Error(), "truncated") {
				t.Fatalf("error = %v, want truncated-block refusal", err)
			}
		})
	}
}

func TestDumpTableToCSVPropagatesReaderFailureInsideCopy(t *testing.T) {
	r := io.MultiReader(
		strings.NewReader("COPY public.users (id) FROM stdin;\n1\n"),
		copyErrReader{err: errors.New("disk read failed")},
	)
	_, _, err := DumpTableToCSV(r, "public.users", &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "disk read failed") {
		t.Fatalf("error = %v, want underlying reader failure", err)
	}
}

func TestPGUnescapeSupportsByteEscapesAndPreservesUnknown(t *testing.T) {
	if got, want := pgUnescape(`A\101\x42\q`), `AAB\q`; got != want {
		t.Fatalf("pgUnescape = %q, want %q", got, want)
	}
}

func TestDumpTableToCSVHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := DumpTableToCSVContext(ctx, strings.NewReader(pgDump), "users", &strings.Builder{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
