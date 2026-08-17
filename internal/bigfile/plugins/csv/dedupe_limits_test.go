package csv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDedupeCanonicalKeysAreUnambiguous(t *testing.T) {
	left := []string{"a\x00b", "c"}
	right := []string{"a", "b\x00c"}
	for _, keyColumn := range []int{-1, 9} {
		if dedupeKey(left, keyColumn) == dedupeKey(right, keyColumn) {
			t.Fatalf("key column %d retained an embedded-NUL collision", keyColumn)
		}
	}
	if dedupeKey([]string{"id", ""}, 1) == dedupeKey([]string{"id"}, 1) {
		t.Fatal("present empty key collided with a missing key")
	}
	if dedupeKey([]string{"1", "same"}, 1) != dedupeKey([]string{"2", "same"}, 1) {
		t.Fatal("equal selected key cells produced different keys")
	}
}

func TestDedupeSetComparesFullKeysOnHashCollision(t *testing.T) {
	set, err := newDedupeSet(4, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	const forcedHash = 42
	first := dedupeKey([]string{"first"}, 0)
	second := dedupeKey([]string{"second"}, 0)
	if added, err := set.addHashed(first, forcedHash, 1); err != nil || !added {
		t.Fatalf("first add = %t, %v", added, err)
	}
	if added, err := set.addHashed(second, forcedHash, 2); err != nil || !added {
		t.Fatalf("colliding distinct add = %t, %v", added, err)
	}
	if added, err := set.addHashed(first, forcedHash, 3); err != nil || added {
		t.Fatalf("duplicate add = %t, %v", added, err)
	}
}

func TestDedupeSetRefusesBeforeStateGrowth(t *testing.T) {
	t.Run("distinct keys", func(t *testing.T) {
		set, err := newDedupeSet(1, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if added, err := set.add(dedupeKey([]string{"first"}, 0), 1); err != nil || !added {
			t.Fatalf("first add = %t, %v", added, err)
		}
		beforeKeys, beforeMemory := set.distinctKeys, set.memoryBytes
		if _, err := set.add(dedupeKey([]string{"second"}, 0), 2); !errors.Is(err, ErrDedupeBudgetExceeded) {
			t.Fatalf("error = %v, want ErrDedupeBudgetExceeded", err)
		}
		if set.distinctKeys != beforeKeys || set.memoryBytes != beforeMemory {
			t.Fatalf("set grew on refusal: keys %d -> %d, memory %d -> %d", beforeKeys, set.distinctKeys, beforeMemory, set.memoryBytes)
		}
	})

	t.Run("memory", func(t *testing.T) {
		first := dedupeKey([]string{"first"}, 0)
		budget := dedupeSetBaseMemoryBytes(4) + dedupeKeyMemoryBytes(len(first))
		set, err := newDedupeSet(4, budget)
		if err != nil {
			t.Fatal(err)
		}
		if added, err := set.add(first, 1); err != nil || !added {
			t.Fatalf("first add = %t, %v", added, err)
		}
		beforeKeys, beforeMemory := set.distinctKeys, set.memoryBytes
		if _, err := set.add(dedupeKey([]string{strings.Repeat("x", 512)}, 0), 2); !errors.Is(err, ErrDedupeBudgetExceeded) {
			t.Fatalf("error = %v, want ErrDedupeBudgetExceeded", err)
		}
		if set.distinctKeys != beforeKeys || set.memoryBytes != beforeMemory {
			t.Fatalf("set grew on refusal: keys %d -> %d, memory %d -> %d", beforeKeys, set.distinctKeys, beforeMemory, set.memoryBytes)
		}
	})
}

func TestDedupeRowsBudgetFailureIsUnpublished(t *testing.T) {
	tests := []struct {
		name string
		opts DedupeOptions
		data string
		kind DedupeBudgetKind
	}{
		{
			name: "distinct keys",
			opts: DedupeOptions{Delimiter: ',', HasHeader: true, KeyColumn: 0, MaxDistinctKeys: 1, MaxMemoryBytes: 1 << 20},
			data: "key\nfirst\nsecond\n",
			kind: DedupeBudgetDistinctKeys,
		},
		{
			name: "memory",
			opts: func() DedupeOptions {
				first := dedupeKey([]string{"small"}, 0)
				return DedupeOptions{
					Delimiter: ',', HasHeader: true, KeyColumn: 0, MaxDistinctKeys: 4,
					MaxMemoryBytes: dedupeSetBaseMemoryBytes(4) + dedupeKeyMemoryBytes(len(first)),
				}
			}(),
			data: "key\nsmall\n" + strings.Repeat("x", 512) + "\n",
			kind: DedupeBudgetMemoryBytes,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "source.csv")
			dst := filepath.Join(dir, "output.csv")
			if err := os.WriteFile(src, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := DedupeRowsFile(context.Background(), src, dst, test.opts)
			if !errors.Is(err, ErrDedupeBudgetExceeded) {
				t.Fatalf("error = %v, want ErrDedupeBudgetExceeded", err)
			}
			var budgetErr *DedupeBudgetError
			if !errors.As(err, &budgetErr) || budgetErr.Kind != test.kind {
				t.Fatalf("budget error = %#v, want kind %q", budgetErr, test.kind)
			}
			if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed dedupe published output: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "source.csv" {
				t.Fatalf("failed dedupe left artifacts: %v", entries)
			}
		})
	}
}

func TestDedupeLimitsRejectBeforeOpeningSource(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "output.csv")
	_, err := DedupeRowsFile(context.Background(), filepath.Join(dir, "missing.csv"), dst, DedupeOptions{
		Delimiter: ',', KeyColumn: 0, MaxDistinctKeys: MaxDedupeDistinctKeys + 1,
	})
	if err == nil || !strings.Contains(err.Error(), "application maximum") {
		t.Fatalf("error = %v, want hard-limit refusal", err)
	}
	if _, statErr := os.Lstat(dst); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid configuration created output: %v", statErr)
	}
}
