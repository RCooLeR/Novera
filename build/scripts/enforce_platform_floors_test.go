package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetPlistString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "updates immediate string",
			body: "<key>Minimum</key>\n\t<string>12.0.0</string>",
			want: "<key>Minimum</key>\n\t<string>13.0.0</string>",
		},
		{
			name: "leaves current value unchanged",
			body: "<key>Minimum</key><string>13.0.0</string>",
			want: "<key>Minimum</key><string>13.0.0</string>",
		},
		{name: "rejects missing key", body: "<dict/>", wantErr: true},
		{
			name:    "rejects duplicate key",
			body:    "<key>Minimum</key><string>12</string><key>Minimum</key><string>12</string>",
			wantErr: true,
		},
		{
			name:    "rejects non-string value",
			body:    "<key>Minimum</key><integer>12</integer>",
			wantErr: true,
		},
		{
			name:    "rejects intervening key",
			body:    "<key>Minimum</key><key>Other</key><string>12</string>",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "Info.plist")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}

			err := setPlistString(path, "Minimum", "13.0.0")
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}
