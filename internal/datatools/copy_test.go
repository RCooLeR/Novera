package datatools

import (
	"strings"
	"testing"
)

const pgDump = "--\n-- PostgreSQL database dump\n--\n" +
	"CREATE TABLE public.users (id integer, name text, bio text);\n" +
	"COPY public.users (id, name, bio) FROM stdin;\n" +
	"1\tAda\tlikes \\t tabs\n" +
	"2\tBob\t\\N\n" +
	"3\tO'Neill\tline\\nbreak\n" +
	"\\.\n" +
	"CREATE TABLE public.orders (id integer);\n" +
	"COPY public.orders (id) FROM stdin;\n" +
	"10\n" +
	"\\.\n"

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
		"2,Bob,\n" + // \N → empty
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
