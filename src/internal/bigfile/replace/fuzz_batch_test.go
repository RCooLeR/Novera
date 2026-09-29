package replace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func FuzzReplaceBatchPlainChunkInvariance(f *testing.F) {
	f.Add("hello abc world abc", "abc", "x", "hello", "hi", false, false, uint8(4), uint8(11))
	f.Add("a aa aaa aaaa", "aa", "b", "aaa", "c", false, false, uint8(2), uint8(9))
	f.Add("word boundary word", "word", "x", "boundary", "y", true, true, uint8(3), uint8(17))
	f.Add("no matches here", "abc", "z", "def", "q", true, false, uint8(1), uint8(5))

	f.Fuzz(func(t *testing.T, source string, findA string, replA string, findB string, replB string, caseInsensitive bool, wholeWord bool, chunkA uint8, chunkB uint8) {
		src := []byte(source)
		find1 := []byte(findA)
		rep1 := []byte(replA)
		find2 := []byte(findB)
		rep2 := []byte(replB)

		if len(src) > 64*1024 || len(find1) > 512 || len(find2) > 512 || len(rep1) > 512 || len(rep2) > 512 {
			t.Skip()
		}
		if !isASCII(src) || !isASCII(find1) || !isASCII(rep1) || !isASCII(find2) || !isASCII(rep2) {
			t.Skip()
		}

		rules := make([]BatchRule, 0, 2)
		if len(find1) > 0 {
			rules = append(rules, BatchRule{Name: "Rule 1", Find: find1, Replace: rep1, Priority: len(rules)})
		}
		if len(find2) > 0 {
			rules = append(rules, BatchRule{Name: "Rule 2", Find: find2, Replace: rep2, Priority: len(rules)})
		}
		if len(rules) == 0 {
			t.Skip()
		}

		chunkSizeA := int(chunkA%64) + 1
		chunkSizeB := int(chunkB%64) + 1
		if chunkSizeA == chunkSizeB {
			chunkSizeB++
		}

		outA, matchesA, conflictsA, err := runBatchPlainForFuzz(t, src, rules, BatchOptions{
			ChunkSize:       chunkSizeA,
			CaseInsensitive: caseInsensitive,
			WholeWord:       wholeWord,
		})
		if err != nil {
			t.Fatalf("run A failed: %v", err)
		}
		outB, matchesB, conflictsB, err := runBatchPlainForFuzz(t, src, rules, BatchOptions{
			ChunkSize:       chunkSizeB,
			CaseInsensitive: caseInsensitive,
			WholeWord:       wholeWord,
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
		if conflictsA != conflictsB {
			t.Fatalf("conflict invariance failed: chunkA=%d chunkB=%d conflictsA=%d conflictsB=%d", chunkSizeA, chunkSizeB, conflictsA, conflictsB)
		}
	})
}

func runBatchPlainForFuzz(t *testing.T, src []byte, rules []BatchRule, opts BatchOptions) ([]byte, int64, int64, error) {
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
	matches, conflicts, err := ReplaceBatchPlain(context.Background(), f, dst, rules, opts)
	if err != nil {
		return nil, 0, 0, err
	}
	return append([]byte(nil), dst.Bytes()...), matches, conflicts, nil
}
