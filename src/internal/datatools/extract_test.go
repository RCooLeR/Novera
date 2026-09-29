package datatools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExtractSQLTableCollectsNonContiguousOwnedSpans(t *testing.T) {
	dump := "SET statement_timeout = 0;\n" +
		"CREATE TABLE public.users (id integer);\n" +
		"CREATE TABLE public.orders (id integer);\n" +
		"ALTER TABLE ONLY public.users ADD CONSTRAINT users_pk PRIMARY KEY (id);\n" +
		"COPY public.orders (id) FROM stdin;\n9\n\\.\n" +
		"COPY public.users (id) FROM stdin;\n1\n\\.\n" +
		"INSERT INTO public.orders VALUES (10);\n" +
		"INSERT INTO public.users VALUES (2);\n" +
		"CREATE INDEX users_id_idx ON public.users (id);\n" +
		"INSERT INTO public.users VALUES (3);\n"

	var out strings.Builder
	n, found, err := ExtractSQLTable(strings.NewReader(dump), "public.users", &out)
	if err != nil {
		t.Fatal(err)
	}
	if !found || n != int64(out.Len()) {
		t.Fatalf("found=%v bytes=%d output=%d", found, n, out.Len())
	}
	got := out.String()
	for _, want := range []string{
		"CREATE TABLE public.users",
		"ALTER TABLE ONLY public.users",
		"COPY public.users",
		"\n1\n\\.\n",
		"INSERT INTO public.users VALUES (2)",
		"CREATE INDEX users_id_idx ON public.users",
		"INSERT INTO public.users VALUES (3)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing owned span %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"statement_timeout", "public.orders", "\n9\n", "VALUES (10)"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("included unrelated span %q in:\n%s", unwanted, got)
		}
	}
}

func TestExtractSQLTableRejectsAmbiguousBareName(t *testing.T) {
	dump := "CREATE TABLE audit.users (id integer);\nCREATE TABLE public.users (id integer);\n"
	var out strings.Builder
	_, _, err := ExtractSQLTable(strings.NewReader(dump), "users", &out)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v, want ambiguity refusal", err)
	}
}

func TestExtractSQLTableRejectsTruncatedCopy(t *testing.T) {
	var out strings.Builder
	_, found, err := ExtractSQLTable(strings.NewReader("COPY public.users (id) FROM stdin;\n1\n"), "public.users", &out)
	if !found || err == nil || !strings.Contains(err.Error(), "truncated COPY") {
		t.Fatalf("found=%v error=%v, want truncated COPY refusal", found, err)
	}
}

func TestExtractSQLTableHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out strings.Builder
	_, _, err := ExtractSQLTableContext(ctx, strings.NewReader("CREATE TABLE t (id integer);\n"), "t", &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if out.Len() != 0 {
		t.Fatalf("canceled extraction wrote %d bytes", out.Len())
	}
}
