package units

import (
	"math"
	"testing"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{"bytes", 42, "42 B"},
		{"kib", 1536, "1.5 KiB"},
		{"mib", 2 * 1024 * 1024, "2.0 MiB"},
		{"gib", 3 * 1024 * 1024 * 1024, "3.0 GiB"},
		{"pib", 1 << 50, "1.0 PiB"},
		{"maximum int64", math.MaxInt64, "8.0 EiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatBytes(tt.in); got != tt.want {
				t.Fatalf("FormatBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatBytesUint(t *testing.T) {
	if got, want := FormatBytesUint(5*1024*1024), "5.0 MiB"; got != want {
		t.Fatalf("FormatBytesUint() = %q, want %q", got, want)
	}
}

func TestParseByteOffset(t *testing.T) {
	tests := []struct {
		name  string
		input string
		size  int64
		want  int64
	}{
		{"raw", "42", 1000, 42},
		{"spaced unit", "2 KB", 10_000, 2048},
		{"attached unit", "1.5MiB", 10_000_000, 1572864},
		{"percent", "25%", 1000, 250},
		{"clamp percent", "150%", 1000, 1000},
		{"clamp offset", "2 GB", 1000, 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseByteOffset(tt.input, tt.size)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseByteOffsetErrors(t *testing.T) {
	tests := []string{"", "-1", "-10%", "12 XB", "abc"}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseByteOffset(input, 1000); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int64
	}{
		{"raw", "42", 42},
		{"spaced unit", "2 KB", 2048},
		{"attached unit", "1.5MiB", 1572864},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseByteSize(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseByteSizeErrors(t *testing.T) {
	tests := []string{"", "0", "-1", "25%", "12 XB", "abc"}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseByteSize(input); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseLineNumber(t *testing.T) {
	got, err := ParseLineNumber(" 42 ")
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}

func TestParseLineNumberErrors(t *testing.T) {
	for _, input := range []string{"", "0", "-1", "abc"} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseLineNumber(input); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
