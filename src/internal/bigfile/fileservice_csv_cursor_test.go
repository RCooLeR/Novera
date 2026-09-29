package bigfile

import (
	"errors"
	"testing"
)

func TestCSVGridCursorRegistryBindsProvenanceAndInvalidatesFile(t *testing.T) {
	t.Parallel()
	var registry csvGridCursorRegistry
	registry.issue("f1", 2, ',', 14)

	if err := registry.validate("f1", 2, ',', 14); err != nil {
		t.Fatalf("issued cursor rejected: %v", err)
	}
	for name, args := range map[string]struct {
		fileID     string
		generation uint64
		delimiter  rune
		offset     int64
	}{
		"unissued":         {"f1", 2, ',', 9},
		"wrong file":       {"f2", 2, ',', 14},
		"wrong generation": {"f1", 3, ',', 14},
		"wrong delimiter":  {"f1", 2, ';', 14},
	} {
		if err := registry.validate(args.fileID, args.generation, args.delimiter, args.offset); !errors.Is(err, ErrCSVGridCursorInvalid) {
			t.Errorf("%s error = %v, want ErrCSVGridCursorInvalid", name, err)
		}
	}
	if err := registry.validate("arbitrary", 0, 0, 0); err != nil {
		t.Fatalf("BOF cursor rejected: %v", err)
	}

	registry.issue("f2", 1, ',', 7)
	registry.invalidateFile("f1")
	if err := registry.validate("f1", 2, ',', 14); !errors.Is(err, ErrCSVGridCursorInvalid) {
		t.Fatalf("invalidated cursor error = %v, want ErrCSVGridCursorInvalid", err)
	}
	if err := registry.validate("f2", 1, ',', 7); err != nil {
		t.Fatalf("unrelated file cursor was invalidated: %v", err)
	}
}

func TestCSVGridCursorRegistryEvictsAtHardLimit(t *testing.T) {
	t.Parallel()
	var registry csvGridCursorRegistry
	for offset := int64(1); offset <= csvGridCursorLimit+1; offset++ {
		registry.issue("f1", 1, ',', offset)
	}
	if got := len(registry.order); got != csvGridCursorLimit {
		t.Fatalf("cursor order length = %d, want %d", got, csvGridCursorLimit)
	}
	if got := len(registry.keys); got != csvGridCursorLimit {
		t.Fatalf("cursor key count = %d, want %d", got, csvGridCursorLimit)
	}
	if err := registry.validate("f1", 1, ',', 1); !errors.Is(err, ErrCSVGridCursorInvalid) {
		t.Fatalf("oldest cursor error = %v, want eviction", err)
	}
	if err := registry.validate("f1", 1, ',', csvGridCursorLimit+1); err != nil {
		t.Fatalf("newest cursor rejected: %v", err)
	}
}
