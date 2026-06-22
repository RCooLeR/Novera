package replace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func FuzzRewriteLineEndingsIdempotent(f *testing.F) {
	f.Add([]byte("alpha\r\nbeta\rgamma\n"), uint8(0))
	f.Add([]byte("no-newlines"), uint8(1))
	f.Add([]byte("\r\r\n\nx"), uint8(2))

	f.Fuzz(func(t *testing.T, src []byte, targetSeed uint8) {
		if len(src) > 64*1024 {
			t.Skip()
		}
		targetName := lineEndingTargetFromSeed(targetSeed)
		target, err := lineEndingBytes(targetName)
		if err != nil {
			t.Fatalf("lineEndingBytes failed: %v", err)
		}

		var out bytes.Buffer
		conversions, err := rewriteLineEndings(context.Background(), bytes.NewReader(src), &out, target, nil)
		if err != nil {
			t.Fatalf("rewriteLineEndings failed: %v", err)
		}

		expectedTokens := int64(countLineEndingTokens(src))
		if conversions != expectedTokens {
			t.Fatalf("conversions=%d want=%d", conversions, expectedTokens)
		}
		if !lineEndingBytesAreCanonical(out.Bytes(), targetName) {
			t.Fatalf("non-canonical output for %s: %q", targetName, out.Bytes())
		}

		// Idempotence: rewriting canonical output to the same target must be byte-identical.
		var out2 bytes.Buffer
		_, err = rewriteLineEndings(context.Background(), bytes.NewReader(out.Bytes()), &out2, target, nil)
		if err != nil {
			t.Fatalf("second rewrite failed: %v", err)
		}
		if !bytes.Equal(out.Bytes(), out2.Bytes()) {
			t.Fatalf("non-idempotent output for %s: first=%q second=%q", targetName, out.Bytes(), out2.Bytes())
		}
	})
}

func TestConvertEncodingFileUTF8ToUTF16ToUTF8Roundtrip(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	midPath := filepath.Join(dir, "mid-utf16.txt")
	outPath := filepath.Join(dir, "out-utf8.txt")

	original := "alpha\r\nbeta\ncafГ©\r\nrow-42\n"
	if err := os.WriteFile(srcPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ConvertEncodingFile(context.Background(), srcPath, midPath, "UTF-16LE", FileOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConvertEncodingFile(context.Background(), midPath, outPath, "UTF-8", FileOptions{}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("roundtrip output = %q, want %q", string(got), original)
	}
}

func lineEndingTargetFromSeed(seed uint8) string {
	switch seed % 3 {
	case 0:
		return "LF"
	case 1:
		return "CRLF"
	default:
		return "CR"
	}
}

func countLineEndingTokens(data []byte) int {
	count := 0
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '\r':
			count++
			if i+1 < len(data) && data[i+1] == '\n' {
				i++
			}
		case '\n':
			count++
		}
	}
	return count
}

func lineEndingBytesAreCanonical(data []byte, target string) bool {
	switch target {
	case "LF":
		return !bytes.Contains(data, []byte{'\r'})
	case "CR":
		return !bytes.Contains(data, []byte{'\n'})
	case "CRLF":
		// Every CR must be followed by LF, and every LF must be preceded by CR.
		for i := 0; i < len(data); i++ {
			if data[i] == '\r' {
				if i+1 >= len(data) || data[i+1] != '\n' {
					return false
				}
				i++
				continue
			}
			if data[i] == '\n' {
				return false
			}
		}
		return true
	default:
		return false
	}
}
