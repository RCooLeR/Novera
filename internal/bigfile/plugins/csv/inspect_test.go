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

func TestInspectReaderTruncatedQuotedCSVDisqualifiesWrongDelimiter(t *testing.T) {
	report, err := InspectReader(strings.NewReader("id,name\n1,\"Ada\"\n2,\"Grace\"\n"), InspectOptions{
		MaxBytes: 22,
		MaxRows:  10,
	})
	if err != nil {
		t.Fatalf("InspectReader() error = %v", err)
	}
	if report.Delimiter != ',' {
		t.Fatalf("delimiter = %q, want comma; candidates = %+v", report.Delimiter, report.Candidates)
	}
	if !report.TruncatedSample {
		t.Fatal("truncated sample was not reported")
	}
}

func TestInspectReaderFieldLimitDisqualifiesOnlyWrongDelimiter(t *testing.T) {
	wideValue := strings.Repeat(",", MaxCSVFieldsPerRecord)
	complete := "id\tpayload\n1\t" + wideValue + "\n2\tok\n3\tfine\n"
	truncated := complete + strings.Repeat("tail", 20)

	tests := []struct {
		name     string
		input    string
		maxBytes int64
	}{
		{name: "complete sample", input: complete},
		{
			name:     "truncated sample",
			input:    truncated,
			maxBytes: int64(strings.Index(truncated, "3\tfine") + 3),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := InspectReader(strings.NewReader(test.input), InspectOptions{
				MaxBytes: test.maxBytes,
				MaxRows:  10,
			})
			if err != nil {
				t.Fatalf("InspectReader() error = %v", err)
			}
			if report.Delimiter != '\t' {
				t.Fatalf("delimiter = %q, want tab; candidates = %+v", report.Delimiter, report.Candidates)
			}
		})
	}
}

func TestInspectReaderRequiresReader(t *testing.T) {
	if _, err := InspectReader(nil, InspectOptions{}); err == nil {
		t.Fatal("expected nil reader error")
	}
}
