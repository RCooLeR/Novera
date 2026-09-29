package replace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fuzzSyncBuffer struct {
	bytes.Buffer
}

func (b *fuzzSyncBuffer) Sync() error {
	return nil
}

func FuzzReplacePlainCaseSensitiveMatchesReference(f *testing.F) {
	f.Add("hello world hello", "hello", "bye", uint16(8))
	f.Add("aaaaaa", "aa", "z", uint16(3))
	f.Add("prefix-middle-suffix", "middle", "M", uint16(5))
	f.Add("abc", "abcdef", "x", uint16(2))

	f.Fuzz(func(t *testing.T, source string, pattern string, repl string, chunkHint uint16) {
		src := []byte(source)
		needle := []byte(pattern)
		replacement := []byte(repl)
		if len(needle) == 0 {
			t.Skip()
		}
		if !isASCII(src) || !isASCII(needle) || !isASCII(replacement) {
			t.Skip()
		}
		if len(src) > 64*1024 || len(needle) > 2048 || len(replacement) > 2048 {
			t.Skip()
		}

		chunkSize := int(chunkHint%256) + 1
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "src.txt")
		if err := os.WriteFile(srcPath, src, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(srcPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = file.Close()
		})

		dst := &fuzzSyncBuffer{}
		matches, err := ReplacePlain(context.Background(), file, dst, needle, replacement, PlainOptions{
			ChunkSize: chunkSize,
		})
		if err != nil {
			t.Fatalf("ReplacePlain failed: %v", err)
		}

		expected := bytes.ReplaceAll(src, needle, replacement)
		expectedMatches := int64(bytes.Count(src, needle))
		if !bytes.Equal(dst.Bytes(), expected) {
			t.Fatalf("output mismatch\nsource=%q\nneedle=%q\nrepl=%q\ngot=%q\nwant=%q", source, pattern, repl, dst.Bytes(), expected)
		}
		if matches != expectedMatches {
			t.Fatalf("matches=%d want=%d", matches, expectedMatches)
		}
	})
}

func FuzzReplacePlainCaseInsensitiveMatchesReference(f *testing.F) {
	f.Add("HeLLo world hello", "hello", "bye", uint16(8))
	f.Add("AbCaBcABc", "abc", "x", uint16(2))
	f.Add("nomatch", "XYZ", "k", uint16(7))
	f.Add("boundary test boundary", "BOUNDARY", "-", uint16(5))

	f.Fuzz(func(t *testing.T, source string, pattern string, repl string, chunkHint uint16) {
		src := []byte(source)
		needle := []byte(pattern)
		replacement := []byte(repl)
		if len(needle) == 0 {
			t.Skip()
		}
		if !isASCII(src) || !isASCII(needle) || !isASCII(replacement) {
			t.Skip()
		}
		if len(src) > 64*1024 || len(needle) > 2048 || len(replacement) > 2048 {
			t.Skip()
		}

		chunkSize := int(chunkHint%256) + 1
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "src.txt")
		if err := os.WriteFile(srcPath, src, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(srcPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = file.Close()
		})

		dst := &fuzzSyncBuffer{}
		matches, err := ReplacePlain(context.Background(), file, dst, needle, replacement, PlainOptions{
			ChunkSize:       chunkSize,
			CaseInsensitive: true,
		})
		if err != nil {
			t.Fatalf("ReplacePlain failed: %v", err)
		}

		expected, expectedMatches := replaceCaseInsensitiveReference(src, needle, replacement)
		if !bytes.Equal(dst.Bytes(), expected) {
			t.Fatalf("output mismatch\nsource=%q\nneedle=%q\nrepl=%q\ngot=%q\nwant=%q", source, pattern, repl, dst.Bytes(), expected)
		}
		if matches != expectedMatches {
			t.Fatalf("matches=%d want=%d", matches, expectedMatches)
		}
	})
}

func replaceCaseInsensitiveReference(src []byte, needle []byte, repl []byte) ([]byte, int64) {
	lowerSrc := toLowerASCII(src)
	lowerNeedle := toLowerASCII(needle)
	if len(lowerNeedle) == 0 {
		return append([]byte(nil), src...), 0
	}

	out := make([]byte, 0, len(src))
	var matches int64
	cursor := 0
	for cursor <= len(src)-len(needle) {
		idx := bytes.Index(lowerSrc[cursor:], lowerNeedle)
		if idx < 0 {
			break
		}
		matchStart := cursor + idx
		out = append(out, src[cursor:matchStart]...)
		out = append(out, repl...)
		matches++
		cursor = matchStart + len(needle)
	}
	out = append(out, src[cursor:]...)
	return out, matches
}

func toLowerASCII(data []byte) []byte {
	out := make([]byte, len(data))
	copy(out, data)
	for i, b := range out {
		if b >= 'A' && b <= 'Z' {
			out[i] = b + ('a' - 'A')
		}
	}
	return out
}

func isASCII(data []byte) bool {
	for _, b := range data {
		if b > 0x7F {
			return false
		}
	}
	return true
}
