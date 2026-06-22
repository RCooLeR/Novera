package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/document"
)

func TestParseBatchPlainRules(t *testing.T) {
	rules, err := ParseBatchPlainRules(`
# comment
alpha => omega
beta -> 
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("len(rules) = %d, want 2", len(rules))
	}
	if string(rules[0].Find) != "alpha" || string(rules[0].Replace) != "omega" {
		t.Fatalf("first rule = %q => %q", rules[0].Find, rules[0].Replace)
	}
	if string(rules[1].Find) != "beta" || string(rules[1].Replace) != "" {
		t.Fatalf("second rule = %q => %q", rules[1].Find, rules[1].Replace)
	}
}

func TestParseBatchRuleSetNamesAndDisabled(t *testing.T) {
	set, err := ParseBatchRuleSet(`
Primary :: alpha => omega
! Disabled :: beta => gone
gamma -> 
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Rules) != 2 {
		t.Fatalf("len(set.Rules) = %d, want 2", len(set.Rules))
	}
	if set.DisabledRules != 1 {
		t.Fatalf("set.DisabledRules = %d, want 1", set.DisabledRules)
	}
	if set.Rules[0].Name != "Primary" {
		t.Fatalf("first name = %q", set.Rules[0].Name)
	}
	if string(set.Rules[0].Find) != "alpha" || string(set.Rules[0].Replace) != "omega" {
		t.Fatalf("first rule = %q => %q", set.Rules[0].Find, set.Rules[0].Replace)
	}
	if set.Rules[1].Name != "Rule 2" {
		t.Fatalf("second name = %q, want Rule 2", set.Rules[1].Name)
	}
}

func TestParseBatchRuleSetAllDisabled(t *testing.T) {
	_, err := ParseBatchRuleSet(`
! One :: alpha => omega
! beta -> 
`)
	if err == nil || err.Error() != "no enabled batch rules" {
		t.Fatalf("err = %v, want no enabled batch rules", err)
	}
}

func TestLoadBatchRuleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.qrules")
	text := "Primary :: alpha => omega\n! Disabled :: beta => gone\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	gotText, set, err := LoadBatchRuleFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotText != text {
		t.Fatalf("got text %q, want %q", gotText, text)
	}
	if len(set.Rules) != 1 || set.DisabledRules != 1 {
		t.Fatalf("set = %#v", set)
	}
}

func TestSaveBatchRuleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.qrules")
	text := "Primary :: alpha => omega\n! Disabled :: beta => gone\n"

	set, err := SaveBatchRuleFile(path, text)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Rules) != 1 || set.DisabledRules != 1 {
		t.Fatalf("set = %#v", set)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != text {
		t.Fatalf("file = %q, want %q", string(got), text)
	}
}

func TestReplaceBatchPlainPriorityAndConflicts(t *testing.T) {
	src, err := os.CreateTemp("", "q-batch-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-batch-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("abc abcd abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte("abc"), Replace: []byte("X"), Priority: 0},
		{Name: "Rule 2", Find: []byte("abcd"), Replace: []byte("Y"), Priority: 1},
	}
	matches, conflicts, err := ReplaceBatchPlain(context.Background(), src, dst, rules, BatchOptions{ChunkSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 3 {
		t.Fatalf("matches = %d, want 3", matches)
	}
	if conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", conflicts)
	}

	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "X Xd X" {
		t.Fatalf("got %q, want %q", string(got), "X Xd X")
	}
}

func TestReplaceBatchPlainBoundaryAndWholeWord(t *testing.T) {
	src, err := os.CreateTemp("", "q-batch-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-batch-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("cat catalog CAT cat."); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte("cat"), Replace: []byte("dog"), Priority: 0},
	}
	matches, conflicts, err := ReplaceBatchPlain(context.Background(), src, dst, rules, BatchOptions{
		ChunkSize:       5,
		CaseInsensitive: true,
		WholeWord:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 3 {
		t.Fatalf("matches = %d, want 3", matches)
	}
	if conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", conflicts)
	}

	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "dog catalog dog dog." {
		t.Fatalf("got %q", string(got))
	}
}

func TestReplaceBatchPlainCaseInsensitiveUsesByteStableASCIIFold(t *testing.T) {
	src, err := os.CreateTemp("", "q-batch-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-batch-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	source := "prefix İxx hello KELVIN Kelvin straße"
	if _, err := src.WriteString(source); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Kelvin", Find: []byte("kelvin"), Replace: []byte("FOUND"), Priority: 0},
	}
	matches, conflicts, err := ReplaceBatchPlain(context.Background(), src, dst, rules, BatchOptions{
		ChunkSize:       8,
		CaseInsensitive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 1 {
		t.Fatalf("matches = %d, want 1", matches)
	}
	if conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", conflicts)
	}

	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	want := "prefix İxx hello FOUND Kelvin straße"
	if string(got) != want {
		t.Fatalf("got %q, want %q", string(got), want)
	}
}

func TestReplaceBatchPlainWholeWordUsesUnicodeBoundaries(t *testing.T) {
	src, err := os.CreateTemp("", "q-batch-unicode-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-batch-unicode-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("кот cat котик _кот кот1 кот."); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Cyrillic", Find: []byte("кот"), Replace: []byte("dog"), Priority: 0},
	}
	matches, conflicts, err := ReplaceBatchPlain(context.Background(), src, dst, rules, BatchOptions{
		ChunkSize: 8,
		WholeWord: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 2 {
		t.Fatalf("matches = %d, want 2", matches)
	}
	if conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", conflicts)
	}

	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "dog cat котик _кот кот1 dog." {
		t.Fatalf("got %q", string(got))
	}
}

func TestPreviewBatchPlain(t *testing.T) {
	r := memReaderAt{data: []byte("alpha hello world and abcde")}
	rules := []BatchRule{
		{Name: "Greeting", Find: []byte("hello"), Replace: []byte("bye"), Priority: 0},
		{Name: "Prefix", Find: []byte("abc"), Replace: []byte("X"), Priority: 0},
		{Name: "Longer", Find: []byte("abcde"), Replace: []byte("Y"), Priority: 1},
	}

	previews, conflicts, err := PreviewBatchPlain(context.Background(), r, rules, PreviewOptions{
		ChunkSize:    4,
		MaxHits:      5,
		PreviewBytes: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 2 {
		t.Fatalf("len(previews) = %d, want 2", len(previews))
	}
	if previews[0].RuleName != "Greeting" {
		t.Fatalf("first rule = %q", previews[0].RuleName)
	}
	if previews[0].After != "alpha bye world" {
		t.Fatalf("first after = %q", previews[0].After)
	}
	if previews[1].Conflicts != 1 {
		t.Fatalf("second conflicts = %d, want 1", previews[1].Conflicts)
	}
	if conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", conflicts)
	}
}

func TestPreviewBatchPlainWholeWordUsesUnicodeBoundaries(t *testing.T) {
	r := memReaderAt{data: []byte("мир мирный _мир мир1 мир.")}
	rules := []BatchRule{
		{Name: "Cyrillic", Find: []byte("мир"), Replace: []byte("world"), Priority: 0},
	}

	previews, conflicts, err := PreviewBatchPlain(context.Background(), r, rules, PreviewOptions{
		ChunkSize:    8,
		MaxHits:      10,
		PreviewBytes: 64,
		WholeWord:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 2 {
		t.Fatalf("len(previews) = %d, want 2", len(previews))
	}
	if conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", conflicts)
	}
	if previews[0].After != "world мирный _мир мир1 мир." {
		t.Fatalf("first after = %q", previews[0].After)
	}
	if previews[1].After != "мир мирный _мир мир1 world." {
		t.Fatalf("second after = %q", previews[1].After)
	}
}

func TestReplaceBatchPlainFileWritesOutputAndManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("abc abcd abc"), 0o600); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte("abc"), Replace: []byte("X"), Priority: 0},
		{Name: "Rule 2", Find: []byte("abcd"), Replace: []byte("Y"), Priority: 1},
	}
	summary, err := ReplaceBatchPlainFile(context.Background(), srcPath, outPath, rules, FileOptions{
		ChunkSize: 4,
	}, BatchOptions{
		ChunkSize: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != 3 {
		t.Fatalf("matches = %d, want 3", summary.Matches)
	}
	if summary.Conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", summary.Conflicts)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "X Xd X" {
		t.Fatalf("output = %q", string(got))
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Operation != "batch-plain-replace" {
		t.Fatalf("manifest operation = %q", manifest.Operation)
	}
	if manifest.ConflictCount != 1 {
		t.Fatalf("manifest conflictCount = %d", manifest.ConflictCount)
	}
}

func TestReplaceBatchPlainFileUsesWholeSourceWhileEditableSliceIsDirty(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "cleanup.sql")
	outPath := filepath.Join(dir, "cleanup-out.sql")
	content := "CREATE DEFINER=`root`@`localhost` TABLE users(id int);\nINSERT INTO users VALUES ('original');\n"
	if err := os.WriteFile(srcPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := document.OpenFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	sliceStart := int64(strings.Index(content, "INSERT INTO"))
	sliceEnd := int64(len(content))
	buf, err := document.LoadInMemoryWindow(doc, sliceStart, sliceEnd, sliceEnd-sliceStart)
	if err != nil {
		t.Fatal(err)
	}
	editedSlice := strings.Replace(buf.Text, "original", "edited", 1)
	if _, _, encoded, err := buf.EncodedReplacement(editedSlice); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(encoded), "original") {
		t.Fatalf("edited replacement still contains original marker: %q", string(encoded))
	}

	rules := []BatchRule{
		{Name: "Remove definer", Find: []byte("DEFINER=`root`@`localhost` "), Replace: nil, Priority: 0},
	}
	summary, err := ReplaceBatchPlainFile(context.Background(), srcPath, outPath, rules, FileOptions{
		ChunkSize: 8,
	}, BatchOptions{
		ChunkSize: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != 1 {
		t.Fatalf("matches = %d, want 1", summary.Matches)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(content, "DEFINER=`root`@`localhost` ", "")
	if string(got) != want {
		t.Fatalf("cleanup output = %q, want %q", string(got), want)
	}
	if strings.Contains(string(got), "edited") {
		t.Fatalf("cleanup leaked dirty editable-slice text: %q", string(got))
	}
	sourceAfter, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != content {
		t.Fatalf("source changed to %q, want immutable original", string(sourceAfter))
	}
}

func TestReplaceBatchPlainFileCancelDeletesPartialWhenRequested(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("alpha beta ", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte("alpha"), Replace: []byte("omega"), Priority: 0},
		{Name: "Rule 2", Find: []byte("beta"), Replace: []byte(""), Priority: 1},
	}
	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ReplaceBatchPlainFile(ctx, srcPath, outPath, rules, FileOptions{
		ChunkSize:             32,
		DeletePartialOnCancel: true,
	}, BatchOptions{
		ChunkSize: 32,
		Progress: func(Progress) {
			if !canceled {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial temp should be deleted, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
}

func TestReplaceBatchPlainFileCancelKeepsPartialWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("alpha beta ", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte("alpha"), Replace: []byte("omega"), Priority: 0},
		{Name: "Rule 2", Find: []byte("beta"), Replace: []byte(""), Priority: 1},
	}
	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ReplaceBatchPlainFile(ctx, srcPath, outPath, rules, FileOptions{
		ChunkSize:             32,
		DeletePartialOnCancel: false,
	}, BatchOptions{
		ChunkSize: 32,
		Progress: func(Progress) {
			if !canceled {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); err != nil {
		t.Fatalf("partial temp should be preserved, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if !strings.Contains(manifest.Error, context.Canceled.Error()) {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}

func TestPreviewBatchRegexp(t *testing.T) {
	r := memReaderAt{data: []byte("alpha hello-42 world and abcde")}
	rules := []BatchRule{
		{Name: "Greeting", Find: []byte(`hello-(\d+)`), Replace: []byte(`bye-$1`), Priority: 0},
		{Name: "Prefix", Find: []byte(`abc`), Replace: []byte("X"), Priority: 0},
		{Name: "Longer", Find: []byte(`abcde`), Replace: []byte("Y"), Priority: 1},
	}

	previews, conflicts, err := PreviewBatchRegexp(context.Background(), r, rules, RegexPreviewOptions{
		ChunkSize:    32,
		MaxHits:      5,
		PreviewBytes: 6,
	}, RegexOptions{
		ChunkSize:      32,
		MaxMatchWindow: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 2 {
		t.Fatalf("len(previews) = %d, want 2", len(previews))
	}
	if previews[0].RuleName != "Greeting" {
		t.Fatalf("first rule = %q", previews[0].RuleName)
	}
	if previews[0].After != "alpha bye-42 world" {
		t.Fatalf("first after = %q", previews[0].After)
	}
	if previews[1].Conflicts != 1 {
		t.Fatalf("second conflicts = %d, want 1", previews[1].Conflicts)
	}
	if conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", conflicts)
	}
}

func TestPreviewBatchRegexpRejectsEmptyMatchPattern(t *testing.T) {
	r := memReaderAt{data: []byte("hello")}
	rules := []BatchRule{
		{Name: "Empty", Find: []byte(`\b`), Replace: []byte("|"), Priority: 0},
	}
	if _, _, err := PreviewBatchRegexp(context.Background(), r, rules, RegexPreviewOptions{}, RegexOptions{}); err == nil {
		t.Fatal("expected empty-match regex rejection")
	}
}

func TestReplaceBatchRegexpPriorityAndConflicts(t *testing.T) {
	src, err := os.CreateTemp("", "q-batch-regex-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-batch-regex-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("abc abcd abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte(`abc`), Replace: []byte("X"), Priority: 0},
		{Name: "Rule 2", Find: []byte(`abcd`), Replace: []byte("Y"), Priority: 1},
	}
	matches, conflicts, err := ReplaceBatchRegexp(context.Background(), src, dst, rules, RegexOptions{
		ChunkSize:      4,
		MaxMatchWindow: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 3 {
		t.Fatalf("matches = %d, want 3", matches)
	}
	if conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", conflicts)
	}

	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "X Xd X" {
		t.Fatalf("got %q, want %q", string(got), "X Xd X")
	}
}

func TestReplaceBatchRegexpRejectsEmptyMatchPattern(t *testing.T) {
	src, err := os.CreateTemp("", "q-batch-regex-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-batch-regex-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Empty", Find: []byte(`a*`), Replace: []byte(""), Priority: 0},
	}
	if _, _, err := ReplaceBatchRegexp(context.Background(), src, dst, rules, RegexOptions{}); err == nil {
		t.Fatal("expected empty-match regex rejection")
	}
}

func TestReplaceBatchRegexpCaptureGroupsAcrossBoundary(t *testing.T) {
	src, err := os.CreateTemp("", "q-batch-regex-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	defer src.Close()

	dst, err := os.CreateTemp("", "q-batch-regex-dst-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	if _, err := src.WriteString("id=41 id=42"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte(`id=(\d+)`), Replace: []byte(`row-$1`), Priority: 0},
	}
	matches, conflicts, err := ReplaceBatchRegexp(context.Background(), src, dst, rules, RegexOptions{
		ChunkSize:      5,
		MaxMatchWindow: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 2 {
		t.Fatalf("matches = %d, want 2", matches)
	}
	if conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", conflicts)
	}

	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "row-41 row-42" {
		t.Fatalf("got %q", string(got))
	}
}

func TestReplaceBatchRegexpFileWritesOutputAndManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte("id=41 id=42"), 0o600); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte(`id=(\d+)`), Replace: []byte(`row-$1`), Priority: 0},
	}
	summary, err := ReplaceBatchRegexpFile(context.Background(), srcPath, outPath, rules, FileOptions{
		ChunkSize: 5,
	}, RegexOptions{
		ChunkSize:      5,
		MaxMatchWindow: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Matches != 2 {
		t.Fatalf("matches = %d, want 2", summary.Matches)
	}
	if summary.Conflicts != 0 {
		t.Fatalf("conflicts = %d, want 0", summary.Conflicts)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "row-41 row-42" {
		t.Fatalf("output = %q", string(got))
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Operation != "batch-regex-replace" {
		t.Fatalf("manifest operation = %q", manifest.Operation)
	}
}

func TestReplaceBatchRegexpFileCancelDeletesPartialWhenRequested(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("id=41 id=42 ", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte(`id=(\d+)`), Replace: []byte(`row-$1`), Priority: 0},
	}
	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ReplaceBatchRegexpFile(ctx, srcPath, outPath, rules, FileOptions{
		ChunkSize:             8,
		DeletePartialOnCancel: true,
	}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
		Progress: func(Progress) {
			if !canceled {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial temp should be deleted, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
}

func TestReplaceBatchRegexpFileCancelKeepsPartialWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("id=41 id=42 ", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte(`id=(\d+)`), Replace: []byte(`row-$1`), Priority: 0},
	}
	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	summary, err := ReplaceBatchRegexpFile(ctx, srcPath, outPath, rules, FileOptions{
		ChunkSize:             8,
		DeletePartialOnCancel: false,
	}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
		Progress: func(Progress) {
			if !canceled {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(outPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(summary.TempPath); err != nil {
		t.Fatalf("partial temp should be preserved, stat err = %v", err)
	}

	manifest := readManifest(t, summary.ManifestPath)
	if manifest.Status != "canceled" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if !strings.Contains(manifest.Error, context.Canceled.Error()) {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}

func TestReplaceBatchPlainWholeWordChunkInvariantAtBoundary(t *testing.T) {
	dir := t.TempDir()
	source := []byte("00 ABC 000000")
	srcPath := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(srcPath, source, 0o600); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte("ABC"), Replace: []byte("0"), Priority: 0},
		{Name: "Rule 2", Find: []byte("0"), Replace: []byte("0"), Priority: 1},
	}
	run := func(chunkSize int) (string, int64, int64) {
		src, err := os.Open(srcPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = src.Close()
		}()
		dst := &fuzzSyncBuffer{}
		matches, conflicts, err := ReplaceBatchPlain(context.Background(), src, dst, rules, BatchOptions{
			ChunkSize:       chunkSize,
			CaseInsensitive: true,
			WholeWord:       true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return dst.String(), matches, conflicts
	}

	outA, matchesA, conflictsA := run(5)
	outB, matchesB, conflictsB := run(12)
	if outA != outB {
		t.Fatalf("chunk-dependent output: chunk5=%q chunk12=%q", outA, outB)
	}
	if outA != "00 0 000000" {
		t.Fatalf("unexpected output: %q", outA)
	}
	if matchesA != matchesB {
		t.Fatalf("chunk-dependent matches: chunk5=%d chunk12=%d", matchesA, matchesB)
	}
	if conflictsA != conflictsB {
		t.Fatalf("chunk-dependent conflicts: chunk5=%d chunk12=%d", conflictsA, conflictsB)
	}
}
