package csv

import (
	"strings"
	"testing"
)

func TestInspectReaderDetectsCommaCSVWithHeader(t *testing.T) {
	report, err := InspectReader(strings.NewReader("id,name,active\n1,Ada,true\n2,Linus,false\n3,Grace,true\n"), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Delimiter != ',' {
		t.Fatalf("delimiter = %q, want comma", report.Delimiter)
	}
	if report.DelimiterName != "comma" {
		t.Fatalf("delimiter name = %q, want comma", report.DelimiterName)
	}
	if report.Columns != 3 {
		t.Fatalf("columns = %d, want 3", report.Columns)
	}
	if !report.HasHeader {
		t.Fatal("expected header detection")
	}
	if report.Confidence != "high" {
		t.Fatalf("confidence = %q, want high", report.Confidence)
	}
}

func TestInspectReaderDetectsTSV(t *testing.T) {
	report, err := InspectReader(strings.NewReader("id\tname\tcity\n1\tAda\tLondon\n2\tGrace\tNYC\n"), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Delimiter != '\t' {
		t.Fatalf("delimiter = %q, want tab", report.Delimiter)
	}
	if report.DelimiterName != "tab" {
		t.Fatalf("delimiter name = %q, want tab", report.DelimiterName)
	}
	if report.Columns != 3 {
		t.Fatalf("columns = %d, want 3", report.Columns)
	}
}

func TestInspectReaderHandlesQuotedDelimiter(t *testing.T) {
	report, err := InspectReader(strings.NewReader("id;note;score\n1;\"a;b\";10\n2;\"c;d\";11\n"), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Delimiter != ';' {
		t.Fatalf("delimiter = %q, want semicolon", report.Delimiter)
	}
	if report.Columns != 3 {
		t.Fatalf("columns = %d, want 3", report.Columns)
	}
}

func TestInspectReaderReportsNoDelimiterForPlainText(t *testing.T) {
	report, err := InspectReader(strings.NewReader("alpha beta gamma\njust text here\n"), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Delimiter != 0 {
		t.Fatalf("delimiter = %q, want none", report.Delimiter)
	}
	if report.Confidence != "none" {
		t.Fatalf("confidence = %q, want none", report.Confidence)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("expected no-delimiter warning")
	}
}

func TestInspectReaderHonorsBoundedSample(t *testing.T) {
	report, err := InspectReader(strings.NewReader("a,b,c\n1,2,3\n4,5,6\n"), InspectOptions{
		MaxBytes: 8,
		MaxRows:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.TruncatedSample {
		t.Fatal("expected truncated sample")
	}
	if report.BytesScanned != 8 {
		t.Fatalf("bytes scanned = %d, want 8", report.BytesScanned)
	}
}

func TestInspectReaderRequiresReader(t *testing.T) {
	if _, err := InspectReader(nil, InspectOptions{}); err == nil {
		t.Fatal("expected nil reader error")
	}
}
