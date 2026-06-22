package replace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func FuzzReplaceBatchRegexpChunkInvariance(f *testing.F) {
	f.Add("abc abcd 12 cat AB", uint8(0), false, uint8(5), uint8(12))
	f.Add("AB ab abc 99 cat cab", uint8(3), true, uint8(4), uint8(17))
	f.Add("no match text", uint8(1), false, uint8(2), uint8(9))

	f.Fuzz(func(t *testing.T, source string, ruleSeed uint8, caseInsensitive bool, chunkA uint8, chunkB uint8) {
		src := []byte(source)
		if len(src) > 64*1024 || !isASCII(src) {
			t.Skip()
		}
		if len(src) > 4096 {
			t.Skip()
		}

		rules := regexFuzzRules(ruleSeed)
		chunkSizeA := int(chunkA%57) + 8
		chunkSizeB := int(chunkB%57) + 8
		if chunkSizeA == chunkSizeB {
			chunkSizeB++
		}
		maxWindow := 8

		outA, matchesA, conflictsA, err := runBatchRegexpForFuzz(t, src, rules, RegexOptions{
			ChunkSize:       chunkSizeA,
			MaxMatchWindow:  maxWindow,
			CaseInsensitive: caseInsensitive,
		})
		if err != nil {
			t.Fatalf("run A failed: %v", err)
		}
		outB, matchesB, conflictsB, err := runBatchRegexpForFuzz(t, src, rules, RegexOptions{
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
		if conflictsA != conflictsB {
			t.Fatalf("conflict invariance failed: chunkA=%d chunkB=%d conflictsA=%d conflictsB=%d", chunkSizeA, chunkSizeB, conflictsA, conflictsB)
		}
	})
}

func FuzzPreviewBatchRegexpConsistentWithReplace(f *testing.F) {
	f.Add("abc abcd 12 cat AB", uint8(0), false, uint8(8))
	f.Add("AB ab abc 99 cat cab", uint8(3), true, uint8(17))
	f.Add("no match text", uint8(1), false, uint8(31))

	f.Fuzz(func(t *testing.T, source string, ruleSeed uint8, caseInsensitive bool, chunkHint uint8) {
		src := []byte(source)
		if len(src) > 64*1024 || !isASCII(src) {
			t.Skip()
		}
		if len(src) > 4096 {
			t.Skip()
		}

		rules := regexFuzzRules(ruleSeed)
		maxWindow := 8
		chunkSize := int(chunkHint%57) + maxWindow

		previewOpts := RegexPreviewOptions{
			ChunkSize:    chunkSize,
			MaxHits:      0,
			PreviewBytes: 16,
		}
		regexOpts := RegexOptions{
			ChunkSize:       chunkSize,
			MaxMatchWindow:  maxWindow,
			CaseInsensitive: caseInsensitive,
		}

		previews, previewConflicts, err := PreviewBatchRegexp(context.Background(), memReaderAt{data: src}, rules, previewOpts, regexOpts)
		if err != nil {
			t.Fatalf("preview failed: %v", err)
		}
		_, matches, conflicts, err := runBatchRegexpForFuzz(t, src, rules, regexOpts)
		if err != nil {
			t.Fatalf("replace failed: %v", err)
		}
		if int64(len(previews)) != matches {
			t.Fatalf("preview/replace match mismatch: previews=%d matches=%d", len(previews), matches)
		}
		if int64(previewConflicts) != conflicts {
			t.Fatalf("preview/replace conflict mismatch: preview=%d replace=%d", previewConflicts, conflicts)
		}
	})
}

func regexFuzzRules(seed uint8) []BatchRule {
	// Keep patterns bounded (2-3 bytes) to avoid window-limit noise in this invariance check.
	switch seed % 4 {
	case 0:
		return []BatchRule{
			{Name: "Rule 1", Find: []byte(`abc`), Replace: []byte("X"), Priority: 0},
			{Name: "Rule 2", Find: []byte(`ab`), Replace: []byte("Y"), Priority: 1},
		}
	case 1:
		return []BatchRule{
			{Name: "Rule 1", Find: []byte(`\d\d`), Replace: []byte("##"), Priority: 0},
			{Name: "Rule 2", Find: []byte(`\d`), Replace: []byte("#"), Priority: 1},
		}
	case 2:
		return []BatchRule{
			{Name: "Rule 1", Find: []byte(`cat`), Replace: []byte("dog"), Priority: 0},
			{Name: "Rule 2", Find: []byte(`ca`), Replace: []byte("C"), Priority: 1},
		}
	default:
		return []BatchRule{
			{Name: "Rule 1", Find: []byte(`AB`), Replace: []byte("!"), Priority: 0},
			{Name: "Rule 2", Find: []byte(`A`), Replace: []byte("?"), Priority: 1},
		}
	}
}

func runBatchRegexpForFuzz(t *testing.T, src []byte, rules []BatchRule, opts RegexOptions) ([]byte, int64, int64, error) {
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
	matches, conflicts, err := ReplaceBatchRegexp(context.Background(), f, dst, rules, opts)
	if err != nil {
		return nil, 0, 0, err
	}
	return append([]byte(nil), dst.Bytes()...), matches, conflicts, nil
}
