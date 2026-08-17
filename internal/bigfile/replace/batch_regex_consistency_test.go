package replace

import (
	"context"
	"testing"
)

func TestPreviewBatchRegexpConsistentWithCollectedMatches(t *testing.T) {
	source := []byte("id=41 abc id=42 abcd id=77")
	r := memReaderAt{data: source}
	rules := []BatchRule{
		{Name: "ID", Find: []byte(`id=(\d\d)`), Replace: []byte(`row-$1`), Priority: 0},
		{Name: "ABC", Find: []byte(`abc`), Replace: []byte("X"), Priority: 1},
		{Name: "ABCD", Find: []byte(`abcd`), Replace: []byte("Y"), Priority: 2},
	}
	regexOpts := RegexOptions{
		ChunkSize:      7,
		MaxMatchWindow: 16,
	}
	previewOpts := RegexPreviewOptions{
		ChunkSize:    7,
		MaxHits:      16,
		PreviewBytes: 12,
	}

	compiled, err := compileRegexBatchRules(rules, false, 16)
	if err != nil {
		t.Fatal(err)
	}
	collected, collectedConflicts, err := collectRegexBatchMatches(context.Background(), r, compiled, regexOpts, previewOpts.MaxHits)
	if err != nil {
		t.Fatal(err)
	}
	previews, previewConflicts, err := PreviewBatchRegexp(context.Background(), r, rules, previewOpts, regexOpts)
	if err != nil {
		t.Fatal(err)
	}

	if len(previews) != len(collected) {
		t.Fatalf("len(previews)=%d len(collected)=%d", len(previews), len(collected))
	}
	if int64(previewConflicts) != collectedConflicts {
		t.Fatalf("conflicts mismatch: preview=%d collected=%d", previewConflicts, collectedConflicts)
	}
	for i := range previews {
		if previews[i].Offset != collected[i].Offset {
			t.Fatalf("offset[%d]=%d want %d", i, previews[i].Offset, collected[i].Offset)
		}
		if previews[i].RuleName != collected[i].rule.Name {
			t.Fatalf("rule[%d]=%q want %q", i, previews[i].RuleName, collected[i].rule.Name)
		}
		if previews[i].Conflicts != collected[i].Conflicts {
			t.Fatalf("preview conflicts[%d]=%d want %d", i, previews[i].Conflicts, collected[i].Conflicts)
		}
	}
}
