package replace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func FuzzReplaceRegexpChunkInvariance(f *testing.F) {
	f.Add("abc abcd 12 cat AB", uint8(0), false, uint8(8), uint8(19))
	f.Add("AA bb 42 cc77", uint8(5), true, uint8(11), uint8(25))
	f.Add("no match text", uint8(2), false, uint8(10), uint8(17))

	f.Fuzz(func(t *testing.T, source string, seed uint8, caseInsensitive bool, chunkA uint8, chunkB uint8) {
		src := []byte(source)
		if len(src) > 64*1024 || len(src) > 4096 || !isASCII(src) {
			t.Skip()
		}
		pattern, repl, maxWindow := regexSingleFuzzCase(seed)
		chunkSizeA := int(chunkA%57) + maxWindow
		chunkSizeB := int(chunkB%57) + maxWindow
		if chunkSizeA == chunkSizeB {
			chunkSizeB++
		}

		outA, matchesA, err := runReplaceRegexpForFuzz(t, src, pattern, repl, RegexOptions{
			ChunkSize:       chunkSizeA,
			MaxMatchWindow:  maxWindow,
			CaseInsensitive: caseInsensitive,
		})
		if err != nil {
			t.Fatalf("run A failed: %v", err)
		}
		outB, matchesB, err := runReplaceRegexpForFuzz(t, src, pattern, repl, RegexOptions{
			ChunkSize:       chunkSizeB,
			MaxMatchWindow:  maxWindow,
			CaseInsensitive: caseInsensitive,
		})
		if err != nil {
			t.Fatalf("run B failed: %v", err)
		}

		if string(outA) != string(outB) {
			t.Fatalf("chunk invariance failed: chunkA=%d chunkB=%d outA=%q outB=%q", chunkSizeA, chunkSizeB, outA, outB)
		}
		if matchesA != matchesB {
			t.Fatalf("match invariance failed: chunkA=%d chunkB=%d matchesA=%d matchesB=%d", chunkSizeA, chunkSizeB, matchesA, matchesB)
		}
	})
}

func FuzzPreviewRegexpConsistentWithReplace(f *testing.F) {
	f.Add("abc abcd 12 cat AB", uint8(0), false, uint8(14))
	f.Add("AA bb 42 cc77", uint8(5), true, uint8(19))
	f.Add("no match text", uint8(2), false, uint8(23))

	f.Fuzz(func(t *testing.T, source string, seed uint8, caseInsensitive bool, chunkHint uint8) {
		src := []byte(source)
		if len(src) > 64*1024 || len(src) > 4096 || !isASCII(src) {
			t.Skip()
		}
		pattern, repl, maxWindow := regexSingleFuzzCase(seed)
		chunkSize := int(chunkHint%57) + maxWindow
		opts := RegexOptions{
			ChunkSize:       chunkSize,
			MaxMatchWindow:  maxWindow,
			CaseInsensitive: caseInsensitive,
		}

		re, err := compileRegex(pattern, caseInsensitive)
		if err != nil {
			t.Fatalf("compile failed: %v", err)
		}
		collected, err := collectRegexpMatches(context.Background(), memReaderAt{data: src}, re, opts, 0)
		if err != nil {
			t.Fatalf("collect failed: %v", err)
		}
		previews, err := PreviewRegexp(context.Background(), memReaderAt{data: src}, pattern, repl, RegexPreviewOptions{
			ChunkSize:    chunkSize,
			MaxHits:      0,
			PreviewBytes: 16,
		}, opts)
		if err != nil {
			t.Fatalf("preview failed: %v", err)
		}
		_, matches, err := runReplaceRegexpForFuzz(t, src, pattern, repl, opts)
		if err != nil {
			t.Fatalf("replace failed: %v", err)
		}
		if len(previews) != len(collected) {
			t.Fatalf("preview/collect mismatch: previews=%d collected=%d", len(previews), len(collected))
		}
		for i := range previews {
			if previews[i].Offset != collected[i].Offset {
				t.Fatalf("offset[%d]=%d want %d", i, previews[i].Offset, collected[i].Offset)
			}
		}
		if int64(len(previews)) != matches {
			t.Fatalf("preview/replace mismatch: previews=%d matches=%d", len(previews), matches)
		}
	})
}

func regexSingleFuzzCase(seed uint8) ([]byte, []byte, int) {
	switch seed % 6 {
	case 0:
		return []byte(`abc`), []byte("X"), 8
	case 1:
		return []byte(`ab`), []byte("Y"), 8
	case 2:
		return []byte(`\d\d`), []byte("##"), 8
	case 3:
		return []byte(`cat`), []byte("dog"), 8
	case 4:
		return []byte(`([a-z]{2})(\d{2})`), []byte(`${2}-${1}`), 12
	default:
		return []byte(`[A-Z]{2}`), []byte("!"), 8
	}
}

func runReplaceRegexpForFuzz(t *testing.T, src []byte, pattern []byte, repl []byte, opts RegexOptions) ([]byte, int64, error) {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = f.Close()
	}()

	dst := &fuzzSyncBuffer{}
	matches, err := ReplaceRegexp(context.Background(), f, dst, pattern, repl, opts)
	if err != nil {
		return nil, 0, err
	}
	return append([]byte(nil), dst.Bytes()...), matches, nil
}
