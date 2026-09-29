package delimiter

import (
	"strings"
	"testing"
)

func TestDetectorStreaming(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		chunks []int
		want   bool
	}{
		{
			name:   "arbitrary indentation and keyword seams",
			input:  strings.Repeat(" \t", 80) + "DeLiMiTeR $$\r\n",
			chunks: []int{31, 1, 7, 2, 64, 3, 1},
			want:   true,
		},
		{
			name:   "BOM and directive split bytewise",
			input:  "\xef\xbb\xbf\tDELIMITER //\r\n",
			chunks: []int{1},
			want:   true,
		},
		{
			name:   "form-feed indentation",
			input:  "\f\vdelimiter ;\n",
			chunks: []int{2, 4, 1},
			want:   true,
		},
		{
			name:   "CRLF resets rejected line",
			input:  "SELECT 'not a directive';\r\n\tDELIMITER ;;\r\n",
			chunks: []int{28, 1, 1, 4},
			want:   true,
		},
		{
			name:   "exact keyword at EOF",
			input:  "delimiter",
			chunks: []int{3, 2},
			want:   true,
		},
		{
			name:   "identifier is not directive",
			input:  "delimiter_value = 1;\n",
			chunks: []int{9, 1},
			want:   false,
		},
		{
			name:   "keyword away from line prefix",
			input:  "SELECT delimiter FROM settings;\n",
			chunks: []int{1},
			want:   false,
		},
		{
			name:   "BOM only recognized at BOF",
			input:  "\r\n\xef\xbb\xbfDELIMITER $$\n",
			chunks: []int{2, 1, 1, 1, 9},
			want:   false,
		},
		{
			name:   "near miss",
			input:  "delimiteX $$\n",
			chunks: []int{1},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var detector Detector
			matched := false
			at := 0
			chunkAt := 0
			for at < len(tt.input) && !matched {
				size := len(tt.input) - at
				if len(tt.chunks) > 0 {
					size = tt.chunks[chunkAt%len(tt.chunks)]
					chunkAt++
					if size > len(tt.input)-at {
						size = len(tt.input) - at
					}
				}
				for _, c := range []byte(tt.input[at : at+size]) {
					if detector.Step(c) {
						matched = true
						break
					}
				}
				at += size
			}
			matched = matched || detector.Finish()
			if matched != tt.want {
				t.Fatalf("matched = %v, want %v", matched, tt.want)
			}
		})
	}
}

func TestDetectorIncompleteBOMDoesNotBecomeDirective(t *testing.T) {
	for _, input := range []string{"\xef", "\xef\xbb", "\xefDELIMITER $$\n", "\xef\xbbDELIMITER $$\n"} {
		var detector Detector
		matched := false
		for _, c := range []byte(input) {
			matched = matched || detector.Step(c)
		}
		if matched || detector.Finish() {
			t.Fatalf("malformed BOM prefix %q became a directive", input)
		}
	}
}
