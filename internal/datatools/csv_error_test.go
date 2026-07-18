package datatools

import (
	"errors"
	"io"
	"strings"
	"testing"
)

var errInjectedCSVRead = errors.New("injected CSV read failure")

// terminalErrorReader supplies complete CSV records and then returns a non-EOF
// storage error. This distinguishes a genuine clean end from the failure paths
// that previously committed a successful, truncated result.
type terminalErrorReader struct {
	reader *strings.Reader
}

func newTerminalErrorReader(data string) *terminalErrorReader {
	return &terminalErrorReader{reader: strings.NewReader(data)}
}

func (r *terminalErrorReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if errors.Is(err, io.EOF) {
		return 0, errInjectedCSVRead
	}
	return n, err
}

func TestCSVConsumersPropagateNonEOFReadFailures(t *testing.T) {
	const input = "id,name\n1,Ada\n2,Lin\n"

	tests := []struct {
		name string
		run  func(io.Reader) error
	}{
		{
			name: "schema inference",
			run: func(r io.Reader) error {
				_, err := InferCSVSchema(r, ',', 100)
				return err
			},
		},
		{
			name: "SQL conversion",
			run: func(r io.Reader) error {
				_, err := ConvertCSVToSQL(r, io.Discard, SQLConvertOptions{
					TableName: "people",
					Columns:   []string{"id", "name"},
				})
				return err
			},
		},
		{
			name: "projection",
			run: func(r io.Reader) error {
				_, err := ProjectCSV(r, io.Discard, ',', []string{"name"})
				return err
			},
		},
		{
			name: "add column",
			run: func(r io.Reader) error {
				_, err := AddCSVColumn(r, io.Discard, ',', "source", "import")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(newTerminalErrorReader(input))
			if !errors.Is(err, errInjectedCSVRead) {
				t.Fatalf("error = %v, want injected read failure", err)
			}
			if !strings.Contains(err.Error(), "CSV data record 3") {
				t.Fatalf("error lacks record context: %v", err)
			}
		})
	}
}
