package replace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"os"
	"strings"
	"testing"

	"novera/internal/bigfile/asciifold"
)

type referenceBatchRule struct {
	rule   compiledBatchRule
	needle []byte
}

func referenceBatchReplace(source []byte, rules []BatchRule, caseInsensitive bool, wholeWord bool) ([]byte, int64, int64) {
	compiled := make([]referenceBatchRule, len(rules))
	for i, original := range rules {
		rule := original
		if rule.Name == "" {
			rule.Name = "Rule"
		}
		if rule.Priority == 0 && i > 0 {
			rule.Priority = i
		}
		needle := rule.Find
		if caseInsensitive {
			needle = asciifold.Fold(needle)
		}
		compiled[i] = referenceBatchRule{rule: compiledBatchRule{BatchRule: rule, order: i}, needle: needle}
	}

	var output bytes.Buffer
	cursor := 0
	var matches int64
	var conflicts int64
	for cursor < len(source) {
		var chosen batchCandidate
		found := false
		for _, rule := range compiled {
			position := cursor
			for position < len(source) {
				index := indexPlain(source[position:], rule.needle, caseInsensitive)
				if index < 0 {
					break
				}
				start := position + index
				if replaceWordBoundaryOK(source, start, len(rule.rule.Find), 0, int64(len(source)), wholeWord) {
					candidate := batchCandidate{start: start, end: start + len(rule.rule.Find), rule: rule.rule}
					if !found || betterBatchCandidate(candidate, chosen) {
						chosen = candidate
						found = true
					}
					break
				}
				position = start + 1
			}
		}
		if !found {
			output.Write(source[cursor:])
			break
		}
		output.Write(source[cursor:chosen.start])
		output.Write(chosen.rule.Replace)
		for _, rule := range compiled {
			position := chosen.start
			for position < len(source) {
				index := indexPlain(source[position:], rule.needle, caseInsensitive)
				if index < 0 {
					break
				}
				start := position + index
				end := start + len(rule.rule.Find)
				if start >= chosen.end {
					break
				}
				if replaceWordBoundaryOK(source, start, len(rule.rule.Find), 0, int64(len(source)), wholeWord) {
					same := start == chosen.start && end == chosen.end && rule.rule.order == chosen.rule.order
					if !same && end > chosen.start {
						conflicts++
					}
				}
				position = start + 1
			}
		}
		matches++
		cursor = chosen.end
	}
	return output.Bytes(), matches, conflicts
}

func runBatchPlainBytes(t *testing.T, source []byte, rules []BatchRule, options BatchOptions, writer syncWriter) (int64, int64, error) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "batch-source-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(source); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	return ReplaceBatchPlain(context.Background(), file, writer, rules, options)
}

func TestBatchAutomatonMatchesBruteForceOracleAcrossSeams(t *testing.T) {
	random := rand.New(rand.NewSource(42))
	for caseIndex := 0; caseIndex < 30; caseIndex++ {
		source := make([]byte, 80)
		alphabet := []byte("abAB _.")
		for i := range source {
			source[i] = alphabet[random.Intn(len(alphabet))]
		}
		rules := make([]BatchRule, 1+random.Intn(8))
		for i := range rules {
			pattern := make([]byte, 1+random.Intn(5))
			for j := range pattern {
				pattern[j] = alphabet[random.Intn(4)]
			}
			rules[i] = BatchRule{Find: pattern, Replace: []byte{byte('0' + i)}, Priority: i}
		}
		caseInsensitive := caseIndex%2 == 0
		wholeWord := caseIndex%3 == 0
		want, wantMatches, wantConflicts := referenceBatchReplace(source, rules, caseInsensitive, wholeWord)
		for _, chunk := range []int{1, 3, 7, 19} {
			writer := &countingSyncWriter{}
			matches, conflicts, err := runBatchPlainBytes(t, source, rules, BatchOptions{
				ChunkSize:       chunk,
				WriteBufferSize: 11,
				CaseInsensitive: caseInsensitive,
				WholeWord:       wholeWord,
			}, writer)
			if err != nil {
				t.Fatalf("case %d chunk %d: %v", caseIndex, chunk, err)
			}
			if !bytes.Equal(writer.Bytes(), want) || matches != wantMatches || conflicts != wantConflicts {
				t.Fatalf("case %d chunk %d: output=%q/%q matches=%d/%d conflicts=%d/%d", caseIndex, chunk, writer.Bytes(), want, matches, wantMatches, conflicts, wantConflicts)
			}
		}
	}
}

func TestBatchAutomatonAggregatesMaximumDuplicateRuleSet(t *testing.T) {
	rules := make([]BatchRule, MaxBatchRules)
	for i := range rules {
		rules[i] = BatchRule{Find: []byte("a"), Replace: []byte("z"), Priority: i}
	}
	set, _, err := compileBatchRules(rules, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.patterns) != 1 || set.patterns[0].multiplicity != MaxBatchRules {
		t.Fatalf("patterns = %#v", set.patterns)
	}
	writer := &countingSyncWriter{}
	matches, conflicts, err := runBatchPlainBytes(t, []byte("aaa"), rules, BatchOptions{ChunkSize: 1}, writer)
	if err != nil {
		t.Fatal(err)
	}
	if writer.String() != "zzz" || matches != 3 || conflicts != 3*(MaxBatchRules-1) {
		t.Fatalf("output=%q matches=%d conflicts=%d", writer.String(), matches, conflicts)
	}
}

func TestBatchAutomatonBoundsManyTinyAndSharedPrefixRules(t *testing.T) {
	tiny := make([]BatchRule, MaxBatchRules)
	for i := range tiny {
		tiny[i] = BatchRule{Find: []byte{byte(i >> 8), byte(i)}, Replace: []byte("x"), Priority: i}
	}
	set, _, err := compileBatchRules(tiny, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.patterns) != MaxBatchRules {
		t.Fatalf("compiled patterns = %d, want %d", len(set.patterns), MaxBatchRules)
	}
	if len(set.nodes) > 1+2*MaxBatchRules || len(set.edges) != len(set.nodes)-1 {
		t.Fatalf("automaton nodes/edges = %d/%d", len(set.nodes), len(set.edges))
	}

	shared := make([]BatchRule, 512)
	for i := range shared {
		shared[i] = BatchRule{Find: append(bytes.Repeat([]byte("a"), i+1), 'b'), Replace: []byte("x"), Priority: i}
	}
	source := bytes.Repeat([]byte("a"), 256*1024)
	writer := &countingSyncWriter{}
	matches, conflicts, err := runBatchPlainBytes(t, source, shared, BatchOptions{ChunkSize: 16 * 1024, WriteBufferSize: 1024}, writer)
	if err != nil {
		t.Fatal(err)
	}
	if matches != 0 || conflicts != 0 || !bytes.Equal(writer.Bytes(), source) {
		t.Fatalf("shared-prefix result matches=%d conflicts=%d output=%d", matches, conflicts, writer.Len())
	}
}

func TestReplaceBatchPlainDenseWritesAreBuffered(t *testing.T) {
	source := bytes.Repeat([]byte("a"), 1_000_000)
	writer := &countingSyncWriter{}
	matches, conflicts, err := runBatchPlainBytes(t, source, []BatchRule{{Find: []byte("a"), Replace: []byte("xy")}}, BatchOptions{
		ChunkSize:       32 * 1024,
		WriteBufferSize: 1024,
	}, writer)
	if err != nil {
		t.Fatal(err)
	}
	if matches != int64(len(source)) || conflicts != 0 {
		t.Fatalf("matches/conflicts = %d/%d", matches, conflicts)
	}
	if writer.Len() != 2*len(source) {
		t.Fatalf("output bytes = %d", writer.Len())
	}
	maximumWrites := (writer.Len()+1023)/1024 + 2
	if writer.writeCalls > maximumWrites || writer.writeCalls >= len(source)/100 {
		t.Fatalf("write calls = %d, maximum %d for %d matches", writer.writeCalls, maximumWrites, matches)
	}
	if !writer.synced {
		t.Fatal("destination was not synced")
	}
}

func TestReplaceBatchPlainTinyBufferDeletionAndLongReplacement(t *testing.T) {
	deleteWriter := &countingSyncWriter{}
	matches, _, err := runBatchPlainBytes(t, bytes.Repeat([]byte("a"), 10_000), []BatchRule{{Find: []byte("a")}}, BatchOptions{ChunkSize: 127, WriteBufferSize: 7}, deleteWriter)
	if err != nil {
		t.Fatal(err)
	}
	if matches != 10_000 || deleteWriter.Len() != 0 || deleteWriter.writeCalls != 0 || !deleteWriter.synced {
		t.Fatalf("delete result: matches=%d len=%d writes=%d synced=%v", matches, deleteWriter.Len(), deleteWriter.writeCalls, deleteWriter.synced)
	}

	long := bytes.Repeat([]byte("x"), MaxBatchReplacementBytes)
	longWriter := &countingSyncWriter{}
	if _, _, err := runBatchPlainBytes(t, []byte("a"), []BatchRule{{Find: []byte("a"), Replace: long}}, BatchOptions{WriteBufferSize: 17}, longWriter); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(longWriter.Bytes(), long) {
		t.Fatalf("long replacement bytes = %d, want %d", longWriter.Len(), len(long))
	}
}

type shortBatchSyncWriter struct {
	writeCalls int
	syncCalls  int
}

func (w *shortBatchSyncWriter) Write(data []byte) (int, error) {
	w.writeCalls++
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

func (w *shortBatchSyncWriter) Sync() error {
	w.syncCalls++
	return nil
}

func TestReplaceBatchPlainPreservesShortWriteAndCancellation(t *testing.T) {
	short := &shortBatchSyncWriter{}
	_, _, err := runBatchPlainBytes(t, bytes.Repeat([]byte("a"), 100), []BatchRule{{Find: []byte("a"), Replace: []byte("xy")}}, BatchOptions{WriteBufferSize: 8}, short)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want io.ErrShortWrite", err)
	}
	if short.syncCalls != 0 {
		t.Fatalf("sync calls = %d after short write", short.syncCalls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	file, fileErr := os.CreateTemp(t.TempDir(), "cancel-source-*")
	if fileErr != nil {
		t.Fatal(fileErr)
	}
	defer file.Close()
	if _, fileErr = file.WriteString(strings.Repeat("a", 1024*1024)); fileErr != nil {
		t.Fatal(fileErr)
	}
	if _, fileErr = file.Seek(0, io.SeekStart); fileErr != nil {
		t.Fatal(fileErr)
	}
	cancelWriter := &cancelOnWriteBatchWriter{cancel: cancel}
	_, _, err = ReplaceBatchPlain(ctx, file, cancelWriter, []BatchRule{{Find: []byte("a"), Replace: []byte("x")}}, BatchOptions{ChunkSize: 1024 * 1024, WriteBufferSize: 1024})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if cancelWriter.syncCalls != 0 {
		t.Fatalf("sync calls = %d after cancellation", cancelWriter.syncCalls)
	}
}

type cancelOnWriteBatchWriter struct {
	bytes.Buffer
	cancel    context.CancelFunc
	syncCalls int
}

func (w *cancelOnWriteBatchWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	w.cancel()
	return n, err
}

func (w *cancelOnWriteBatchWriter) Sync() error {
	w.syncCalls++
	return nil
}
