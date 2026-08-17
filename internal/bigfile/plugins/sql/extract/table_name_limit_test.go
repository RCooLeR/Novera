package extract

import (
	"errors"
	"strings"
	"testing"

	"novera/internal/bigfile/plugins/sql/analyze"
)

func TestPlanExtractTableBoundsLookupName(t *testing.T) {
	maximum := strings.Repeat("x", analyze.MaxIdentifierBytes)
	summary := analyze.Summary{Tables: []analyze.Table{{Name: maximum, CreateOffset: 0, InsertOffset: -1}}}
	if _, err := PlanExtractTable(summary, 10, maximum, PlanOptions{}); err != nil {
		t.Fatalf("maximum table name rejected: %v", err)
	}

	tooLarge := strings.Repeat("x", MaxTableSelectionBytes+1)
	if _, err := PlanExtractTable(summary, 10, tooLarge, PlanOptions{}); !errors.Is(err, ErrTableNameTooLong) {
		t.Fatalf("maximum+1 table name error = %v, want %v", err, ErrTableNameTooLong)
	}
}
