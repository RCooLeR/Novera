package bigfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/encodingx"
)

func TestPrimaryWindowsDecodeSupportedEncodingsWithExactRawOffsets(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		text     string
	}{
		{name: "utf8", encoding: "UTF-8", text: "первая 😀\r\nsecond 漢字\r\n"},
		{name: "utf16le", encoding: "UTF-16LE", text: "первая 😀\r\nsecond 漢字\r\n"},
		{name: "utf16be", encoding: "UTF-16BE", text: "первая 😀\r\nsecond 漢字\r\n"},
		{name: "windows1251", encoding: "Windows-1251", text: "первая строка\r\nвторая строка\r\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := encodingx.EncodeString(test.encoding, test.text)
			if err != nil {
				t.Fatal(err)
			}
			bom := encodingx.BOMBytes(test.encoding)
			raw := append(append([]byte(nil), bom...), encoded...)
			service, meta, _ := openPrimaryWindowFixture(t, raw)
			if meta.Encoding != test.encoding {
				t.Fatalf("detected encoding = %q, want %q", meta.Encoding, test.encoding)
			}

			secondText := strings.Index(test.text, "second")
			if secondText < 0 {
				secondText = strings.Index(test.text, "вторая")
			}
			prefix, err := encodingx.EncodeString(test.encoding, test.text[:secondText])
			if err != nil {
				t.Fatal(err)
			}
			secondOffset := int64(len(bom) + len(prefix))
			window, err := service.GetWindow(meta.FileID, secondOffset+2, 256)
			if err != nil {
				t.Fatal(err)
			}
			if window.Text != "second 漢字" && window.Text != "вторая строка" {
				t.Fatalf("decoded window = %q", window.Text)
			}
			if strings.ContainsRune(window.Text, '\uFFFD') {
				t.Fatalf("decoded window contains a replacement rune: %q", window.Text)
			}
			if window.StartByte != secondOffset || len(window.LineOffsets) != 1 || window.LineOffsets[0] != secondOffset {
				t.Fatalf("raw offsets = start %d lines %v, want %d", window.StartByte, window.LineOffsets, secondOffset)
			}
			if window.NextByte != int64(len(raw)) || !window.AtEOF || window.NextByte-window.StartByte > 256 {
				t.Fatalf("window bounds = %+v", window)
			}
		})
	}
}

func TestPrimaryWindowAlternatingNavigationRoundTripsAllEncodings(t *testing.T) {
	fixtures := []struct {
		name     string
		encoding string
		text     string
	}{
		{name: "utf8", encoding: "UTF-8", text: "α😀 one\rβ two\r\n漢 three\nfour 😀 five\r\nsix"},
		{name: "utf16le", encoding: "UTF-16LE", text: "α😀 one\rβ two\r\n漢 three\nfour 😀 five\r\nsix"},
		{name: "utf16be", encoding: "UTF-16BE", text: "α😀 one\rβ two\r\n漢 three\nfour 😀 five\r\nsix"},
		{name: "windows1251", encoding: "Windows-1251", text: "один\rдва\r\nтри\nчетыре\r\nпять"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			encoded, err := encodingx.EncodeString(fixture.encoding, fixture.text)
			if err != nil {
				t.Fatal(err)
			}
			raw := append(append([]byte(nil), encodingx.BOMBytes(fixture.encoding)...), encoded...)
			service, meta, _ := openPrimaryWindowFixture(t, raw)

			const budget = 17
			forward := make([]Window, 0, 16)
			window, err := service.GetWindow(meta.FileID, 0, budget)
			if err != nil {
				t.Fatal(err)
			}
			forward = append(forward, window)
			for !window.AtEOF {
				next, nextErr := service.GetNextWindow(meta.FileID, window.NextByte, budget)
				if nextErr != nil {
					t.Fatal(nextErr)
				}
				if next.StartByte != window.NextByte || next.NextByte <= next.StartByte {
					t.Fatalf("non-progressing forward pair: %+v then %+v", window, next)
				}
				if next.NextByte-next.StartByte > budget {
					t.Fatalf("window [%d,%d) exceeds budget %d", next.StartByte, next.NextByte, budget)
				}
				if strings.ContainsRune(next.Text, '\uFFFD') {
					t.Fatalf("decoded window contains a replacement rune: %q", next.Text)
				}
				forward = append(forward, next)
				window = next
				if len(forward) > 100 {
					t.Fatal("forward navigation did not reach EOF")
				}
			}
			for index := len(forward) - 1; index > 0; index-- {
				previous, previousErr := service.GetPrevWindow(meta.FileID, forward[index].StartByte, budget)
				if previousErr != nil {
					t.Fatal(previousErr)
				}
				want := forward[index-1]
				if previous.StartByte != want.StartByte || previous.NextByte != forward[index].StartByte || previous.Text != want.Text {
					t.Fatalf(
						"reverse page %d = [%d,%d) %q, want [%d,%d) %q",
						index,
						previous.StartByte,
						previous.NextByte,
						previous.Text,
						want.StartByte,
						want.NextByte,
						want.Text,
					)
				}
			}
		})
	}
}

func TestPrimaryWindowsRejectInvalidBudgetsAndChangedSources(t *testing.T) {
	service, meta, path := openPrimaryWindowFixture(t, []byte("first\nsecond\n"))
	if _, err := service.GetWindow(meta.FileID, 0, -1); err == nil {
		t.Fatal("negative budget unexpectedly succeeded")
	}
	if _, err := service.GetWindow(meta.FileID, 0, defaultWindowBytes+1); err == nil {
		t.Fatal("oversized budget unexpectedly succeeded")
	}
	utf16, err := encodingx.EncodeString("UTF-16LE", strings.Repeat("wide ", 20))
	if err != nil {
		t.Fatal(err)
	}
	utf16 = append(encodingx.BOMBytes("UTF-16LE"), utf16...)
	utf16Service, utf16Meta, _ := openPrimaryWindowFixture(t, utf16)
	if _, err := utf16Service.GetWindow(utf16Meta.FileID, 0, 1); err == nil {
		t.Fatal("one-byte UTF-16 budget unexpectedly succeeded")
	}

	if err := os.WriteFile(path, []byte("other\nsource\nexpanded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetWindow(meta.FileID, 0, 64); !errors.Is(err, document.ErrSourceChanged) {
		t.Fatalf("changed-source error = %v, want ErrSourceChanged", err)
	}
}

func TestPrimaryPreviousWindowHonorsLineLimitAndAdjacency(t *testing.T) {
	raw := bytes.Repeat([]byte("x\n"), windowLineTarget+50)
	service, meta, _ := openPrimaryWindowFixture(t, raw)
	first, err := service.GetWindow(meta.FileID, 0, defaultWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.LineOffsets) != windowLineTarget {
		t.Fatalf("rows = %d, want %d", len(first.LineOffsets), windowLineTarget)
	}
	second, err := service.GetNextWindow(meta.FileID, first.NextByte, defaultWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := service.GetPrevWindow(meta.FileID, second.StartByte, defaultWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	if previous.StartByte != first.StartByte || previous.NextByte != second.StartByte {
		t.Fatalf(
			"line-limited previous = [%d,%d), want [%d,%d)",
			previous.StartByte,
			previous.NextByte,
			first.StartByte,
			second.StartByte,
		)
	}
}

func TestPreviousAndTailWindowsConsumeDenseRowsFollowedByLongLine(t *testing.T) {
	raw := bytes.Repeat([]byte("x\n"), windowLineTarget-1)
	raw = append(raw, bytes.Repeat([]byte("long"), 3_000)...)
	raw = append(raw, '\n')
	service, meta, _ := openPrimaryWindowFixture(t, raw)

	previous, err := service.GetPrevWindow(meta.FileID, int64(len(raw)), defaultWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	if previous.StartByte != 0 || previous.NextByte != int64(len(raw)) ||
		len(previous.LineOffsets) != windowLineTarget {
		t.Fatalf("previous mixed window = %+v", previous)
	}

	tail, err := service.GetTailWindow(meta.FileID, defaultWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	if tail.StartByte != 0 || tail.NextByte != int64(len(raw)) ||
		len(tail.LineOffsets) != windowLineTarget || !tail.AtEOF {
		t.Fatalf("tail mixed window = %+v", tail)
	}
}

func TestTailWindowAcceptsBlankLinesAndBOMOnlySources(t *testing.T) {
	utf16Newline, err := encodingx.EncodeString("UTF-16LE", "\r\n")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "LF", raw: []byte("\n")},
		{name: "CRLF", raw: []byte("\r\n")},
		{
			name: "UTF-16LE CRLF",
			raw:  append(encodingx.BOMBytes("UTF-16LE"), utf16Newline...),
		},
		{name: "UTF-8 BOM only", raw: encodingx.BOMBytes("UTF-8")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, meta, _ := openPrimaryWindowFixture(t, test.raw)
			window, err := service.GetTailWindow(meta.FileID, 64)
			if err != nil {
				t.Fatal(err)
			}
			if window.StartByte < 0 || window.NextByte != int64(len(test.raw)) ||
				window.NextByte <= window.StartByte || !window.AtEOF {
				t.Fatalf("tail window = %+v, want bounded progress through %d bytes", window, len(test.raw))
			}
		})
	}
}

func openPrimaryWindowFixture(t *testing.T, raw []byte) (*FileService, FileMeta, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "window.txt")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.CloseFile(meta.FileID)
	})
	return service, meta, path
}
