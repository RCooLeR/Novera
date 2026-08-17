// Package schemadiff compares bounded, structurally parsed CREATE TABLE
// statements. It normalizes ordinary SQL comments, syntax whitespace, known
// keyword case, table-constraint/index declaration order, and recognized table
// option order. Literal contents and identifier case remain exact. Quote
// wrappers normalize only for portable lowercase non-keyword identifiers;
// mixed-case or dialect-sensitive quoting remains distinct. Column-clause and
// expression order is compared conservatively rather than rewritten.
//
// Truncated, over-budget, dialect-ambiguous, or unsupported statements produce
// explicit unknown table diffs and never increase UnchangedCount.
package schemadiff

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"sort"
)

const (
	// MaxStatementBytes bounds every CREATE TABLE statement accepted by the
	// parser, including comments and literals.
	MaxStatementBytes = 2 << 20
	// MaxTokenCount bounds lexer metadata independently of statement length.
	MaxTokenCount = 131072
	// MaxColumnCount bounds materialized column definitions per table.
	MaxColumnCount = 4096
	// MaxSchemaEntryCount includes columns, constraints, and indexes.
	MaxSchemaEntryCount = 8192
	// MaxTableOptionCount bounds parsed table option metadata.
	MaxTableOptionCount = 256
	// MaxNestingDepth bounds expression/type/constraint parenthesis nesting.
	MaxNestingDepth = 128
	// MaxIdentifierBytes prevents hostile identifiers from dominating results.
	MaxIdentifierBytes = 1024
	// MaxDefinitionBytes bounds any one column, constraint, index, or option.
	MaxDefinitionBytes = 256 << 10
	// MaxDiffTableCount bounds table indexing and result construction per side.
	MaxDiffTableCount = 10000
	// MaxDiffDefinitionBytes bounds aggregate schema text inspected by one Diff.
	MaxDiffDefinitionBytes = 64 << 20
)

// ParseStatus reports whether a CREATE TABLE statement was understood in full.
type ParseStatus string

const (
	ParseComplete ParseStatus = "complete"
	ParseUnknown  ParseStatus = "unknown"
)

// DiffStatus reports whether a returned table is changed or cannot be compared
// safely. Unchanged tables are counted and do not appear in ChangedTables.
type DiffStatus string

const (
	DiffChanged DiffStatus = "changed"
	DiffUnknown DiffStatus = "unknown"
)

// Column is one parsed column of a CREATE TABLE statement. Definition retains
// literal and identifier spelling while normalizing comments and syntax-only
// whitespace for readable diff output.
type Column struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`

	canonical string
	identity  string
}

// SchemaElement is a table-level constraint or index.
type SchemaElement struct {
	Definition string `json:"definition"`

	canonical string
}

// TableOption is one normalized table option. Option ordering is cosmetic;
// names and values remain semantically compared.
type TableOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`

	canonical string
}

// ParsedTable is the complete bounded parse of one CREATE TABLE statement.
type ParsedTable struct {
	Items       []Column        `json:"items"`
	Constraints []SchemaElement `json:"constraints"`
	Indexes     []SchemaElement `json:"indexes"`
	Options     []TableOption   `json:"options"`
	Status      ParseStatus     `json:"status"`
	Reason      string          `json:"reason,omitempty"`

	tableIdentity string
	tableDisplay  string
	modifiers     string
	parsed        bool
	integrity     [sha256.Size]byte
}

// Table is one parsed table schema. Columns remains the compatibility surface
// for column-only callers; Definition carries the complete semantic parse.
type Table struct {
	Name       string      `json:"name"`
	Columns    []Column    `json:"columns"`
	Definition ParsedTable `json:"definition"`
}

// ColumnChange describes a column that exists in both tables but differs.
type ColumnChange struct {
	Name string `json:"name"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// OptionChange describes a table option or CREATE modifier change.
type OptionChange struct {
	Name string `json:"name"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// IdentityChange reports that the same analyzer key resolved to different
// qualified table identifiers in the parsed DDL.
type IdentityChange struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// TableDiff is the per-table structural diff. Status unknown is deliberately
// carried in ChangedTables so current service consumers cannot mistake an
// incomplete parse for equality.
type TableDiff struct {
	Name               string          `json:"name"`
	Status             DiffStatus      `json:"status"`
	Reason             string          `json:"reason,omitempty"`
	IdentityChanged    *IdentityChange `json:"identityChanged,omitempty"`
	AddedColumns       []Column        `json:"addedColumns"`
	RemovedColumns     []Column        `json:"removedColumns"`
	ChangedColumns     []ColumnChange  `json:"changedColumns"`
	ColumnOrderChanged bool            `json:"columnOrderChanged"`
	OldColumnOrder     []string        `json:"oldColumnOrder"`
	NewColumnOrder     []string        `json:"newColumnOrder"`
	AddedConstraints   []string        `json:"addedConstraints"`
	RemovedConstraints []string        `json:"removedConstraints"`
	AddedIndexes       []string        `json:"addedIndexes"`
	RemovedIndexes     []string        `json:"removedIndexes"`
	ChangedOptions     []OptionChange  `json:"changedOptions"`
}

// Changed reports whether this table differs or could not be compared safely.
func (d TableDiff) Changed() bool {
	return d.Status == DiffUnknown || d.IdentityChanged != nil ||
		len(d.AddedColumns) > 0 || len(d.RemovedColumns) > 0 || len(d.ChangedColumns) > 0 ||
		d.ColumnOrderChanged || len(d.AddedConstraints) > 0 || len(d.RemovedConstraints) > 0 ||
		len(d.AddedIndexes) > 0 || len(d.RemovedIndexes) > 0 || len(d.ChangedOptions) > 0
}

// Result is the full schema diff between two dumps (A = old, B = new).
type Result struct {
	AddedTables    []string    `json:"addedTables"`
	RemovedTables  []string    `json:"removedTables"`
	ChangedTables  []TableDiff `json:"changedTables"`
	UnchangedCount int         `json:"unchangedCount"`
}

// Diff compares two bounded sets of parsed tables. Table and column identifiers
// are case-sensitive because the package has no dialect/server configuration
// with which to prove that case folding is safe.
func Diff(a, b []Table) Result {
	result, _ := DiffContext(context.Background(), a, b)
	return result
}

// DiffContext is the cancellable form of Diff. Cancellation is returned as an
// error rather than encoded as an unknown schema, preventing lifecycle or user
// cancellation from being mistaken for a completed comparison.
func DiffContext(ctx context.Context, a, b []Table) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	res := newResult()
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if len(a) > MaxDiffTableCount || len(b) > MaxDiffTableCount {
		res.ChangedTables = append(res.ChangedTables, unknownTableDiff("(schema)", fmt.Sprintf(
			"table count exceeds limit of %d (old=%d, new=%d)", MaxDiffTableCount, len(a), len(b))))
		return res, nil
	}
	withinBudget, err := tableSetsWithinByteBudgetContext(ctx, a, b, MaxDiffDefinitionBytes)
	if err != nil {
		return newResult(), err
	}
	if !withinBudget {
		res.ChangedTables = append(res.ChangedTables, unknownTableDiff("(schema)", fmt.Sprintf(
			"aggregate schema definitions exceed %d-byte diff limit", MaxDiffDefinitionBytes)))
		return res, nil
	}

	am, adup := indexTables(a)
	if err := ctx.Err(); err != nil {
		return newResult(), err
	}
	bm, bdup := indexTables(b)
	keys := make([]string, 0, len(am)+len(bm)+len(adup)+len(bdup))
	seen := make(map[string]struct{}, cap(keys))
	for key := range am {
		seen[key] = struct{}{}
	}
	for key := range bm {
		seen[key] = struct{}{}
	}
	for key := range adup {
		seen[key] = struct{}{}
	}
	for key := range bdup {
		seen[key] = struct{}{}
	}
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for index, key := range keys {
		if index%64 == 0 {
			if err := ctx.Err(); err != nil {
				return newResult(), err
			}
		}
		at, inA := am[key]
		bt, inB := bm[key]
		if adup[key] || bdup[key] {
			res.ChangedTables = append(res.ChangedTables, unknownTableDiff(key, "duplicate table identity prevents a unique comparison"))
			continue
		}
		switch {
		case !inA && inB:
			res.AddedTables = append(res.AddedTables, bt.Name)
		case inA && !inB:
			res.RemovedTables = append(res.RemovedTables, at.Name)
		case inA && inB:
			td := diffTable(key, at, bt)
			if td.Changed() {
				if td.Status == "" {
					td.Status = DiffChanged
				}
				res.ChangedTables = append(res.ChangedTables, td)
			} else {
				res.UnchangedCount++
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return newResult(), err
	}
	return res, nil
}

func tableSetsWithinByteBudget(a, b []Table, limit int) bool {
	withinBudget, _ := tableSetsWithinByteBudgetContext(context.Background(), a, b, limit)
	return withinBudget
}

func tableSetsWithinByteBudgetContext(ctx context.Context, a, b []Table, limit int) (bool, error) {
	remaining := limit
	consume := func(value string) bool {
		if len(value) > remaining {
			return false
		}
		remaining -= len(value)
		return true
	}
	checked := 0
	for _, tables := range [][]Table{a, b} {
		for _, table := range tables {
			if checked%64 == 0 {
				if err := ctx.Err(); err != nil {
					return false, err
				}
			}
			checked++
			if len(table.Definition.Items) > MaxColumnCount ||
				len(table.Definition.Constraints)+len(table.Definition.Indexes) > MaxSchemaEntryCount ||
				len(table.Definition.Options) > MaxTableOptionCount || len(table.Columns) > MaxColumnCount {
				return false, nil
			}
			if !consume(table.Name) || !consume(table.Definition.Reason) {
				return false, nil
			}
			for _, column := range table.Definition.Items {
				if !consume(column.Name) || !consume(column.Definition) {
					return false, nil
				}
			}
			for _, constraint := range table.Definition.Constraints {
				if !consume(constraint.Definition) {
					return false, nil
				}
			}
			for _, index := range table.Definition.Indexes {
				if !consume(index.Definition) {
					return false, nil
				}
			}
			for _, option := range table.Definition.Options {
				if !consume(option.Name) || !consume(option.Value) {
					return false, nil
				}
			}
			for _, column := range table.Columns {
				if !consume(column.Name) || !consume(column.Definition) {
					return false, nil
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func newResult() Result {
	return Result{
		AddedTables:   []string{},
		RemovedTables: []string{},
		ChangedTables: []TableDiff{},
	}
}

func newTableDiff(name string) TableDiff {
	return TableDiff{
		Name:               name,
		AddedColumns:       []Column{},
		RemovedColumns:     []Column{},
		ChangedColumns:     []ColumnChange{},
		OldColumnOrder:     []string{},
		NewColumnOrder:     []string{},
		AddedConstraints:   []string{},
		RemovedConstraints: []string{},
		AddedIndexes:       []string{},
		RemovedIndexes:     []string{},
		ChangedOptions:     []OptionChange{},
	}
}

func unknownTableDiff(name string, reason string) TableDiff {
	td := newTableDiff(name)
	td.Status = DiffUnknown
	td.Reason = reason
	return td
}

func diffTable(name string, a, b Table) TableDiff {
	td := newTableDiff(name)
	oldDefinition := tableDefinition(a)
	newDefinition := tableDefinition(b)
	if oldDefinition.Status != ParseComplete || newDefinition.Status != ParseComplete {
		td.Status = DiffUnknown
		td.Reason = parseComparisonReason(oldDefinition, newDefinition)
		return td
	}

	if oldDefinition.tableIdentity != "" && newDefinition.tableIdentity != "" && oldDefinition.tableIdentity != newDefinition.tableIdentity {
		td.IdentityChanged = &IdentityChange{Old: oldDefinition.tableDisplay, New: newDefinition.tableDisplay}
	}

	ac, adup := indexColumns(oldDefinition.Items)
	bc, bdup := indexColumns(newDefinition.Items)
	if adup != "" || bdup != "" {
		td.Status = DiffUnknown
		td.Reason = "duplicate column identity prevents a unique comparison"
		return td
	}
	for _, col := range newDefinition.Items {
		if _, ok := ac[columnIdentity(col)]; !ok {
			td.AddedColumns = append(td.AddedColumns, col)
		}
	}
	for _, col := range oldDefinition.Items {
		bcol, ok := bc[columnIdentity(col)]
		if !ok {
			td.RemovedColumns = append(td.RemovedColumns, col)
			continue
		}
		if col.canonical != bcol.canonical {
			td.ChangedColumns = append(td.ChangedColumns, ColumnChange{Name: col.Name, Old: col.Definition, New: bcol.Definition})
		}
	}

	if sameColumnSet(ac, bc) {
		oldOrder := columnOrder(oldDefinition.Items)
		newOrder := columnOrder(newDefinition.Items)
		if !equalStrings(oldOrder, newOrder) {
			td.ColumnOrderChanged = true
			td.OldColumnOrder = oldOrder
			td.NewColumnOrder = newOrder
		}
	}

	td.AddedConstraints, td.RemovedConstraints = diffElements(oldDefinition.Constraints, newDefinition.Constraints)
	td.AddedIndexes, td.RemovedIndexes = diffElements(oldDefinition.Indexes, newDefinition.Indexes)
	td.ChangedOptions = diffOptions(oldDefinition, newDefinition)
	if td.Changed() {
		td.Status = DiffChanged
	}
	return td
}

func tableDefinition(table Table) ParsedTable {
	if table.Definition.Status != "" {
		if table.Definition.Status == ParseComplete && (!table.Definition.parsed || table.Definition.integrity != parsedTableIntegrity(table.Definition)) {
			return unknownParsedTable("parsed table definition was constructed or mutated outside ParseTable")
		}
		return table.Definition
	}
	return unknownParsedTable("complete table definition unavailable; parse with ParseTable")
}

func parsedTableIntegrity(parsed ParsedTable) [sha256.Size]byte {
	digest := sha256.New()
	writeIntegrityString(digest, string(parsed.Status))
	writeIntegrityString(digest, parsed.Reason)
	writeIntegrityCount(digest, len(parsed.Items))
	for _, column := range parsed.Items {
		writeIntegrityString(digest, column.Name)
		writeIntegrityString(digest, column.Definition)
	}
	writeIntegrityCount(digest, len(parsed.Constraints))
	for _, constraint := range parsed.Constraints {
		writeIntegrityString(digest, constraint.Definition)
	}
	writeIntegrityCount(digest, len(parsed.Indexes))
	for _, index := range parsed.Indexes {
		writeIntegrityString(digest, index.Definition)
	}
	writeIntegrityCount(digest, len(parsed.Options))
	for _, option := range parsed.Options {
		writeIntegrityString(digest, option.Name)
		writeIntegrityString(digest, option.Value)
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func writeIntegrityString(digest hash.Hash, value string) {
	writeIntegrityCount(digest, len(value))
	_, _ = digest.Write([]byte(value))
}

func writeIntegrityCount(digest hash.Hash, value int) {
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], uint64(value))
	_, _ = digest.Write(encoded[:])
}

func parseComparisonReason(a, b ParsedTable) string {
	switch {
	case a.Status != ParseComplete && b.Status != ParseComplete:
		return fmt.Sprintf("old schema unknown: %s; new schema unknown: %s", nonEmptyReason(a.Reason), nonEmptyReason(b.Reason))
	case a.Status != ParseComplete:
		return "old schema unknown: " + nonEmptyReason(a.Reason)
	default:
		return "new schema unknown: " + nonEmptyReason(b.Reason)
	}
}

func nonEmptyReason(reason string) string {
	if reason == "" {
		return "CREATE TABLE definition was not provided"
	}
	return reason
}

func indexTables(tables []Table) (map[string]Table, map[string]bool) {
	indexed := make(map[string]Table, len(tables))
	duplicates := make(map[string]bool)
	for _, table := range tables {
		key := table.Name
		if key == "" {
			key = table.Definition.tableDisplay
		}
		if _, exists := indexed[key]; exists {
			duplicates[key] = true
			continue
		}
		indexed[key] = table
	}
	return indexed, duplicates
}

func indexColumns(columns []Column) (map[string]Column, string) {
	indexed := make(map[string]Column, len(columns))
	for _, column := range columns {
		key := columnIdentity(column)
		if _, exists := indexed[key]; exists {
			return indexed, column.Name
		}
		indexed[key] = column
	}
	return indexed, ""
}

func columnIdentity(column Column) string {
	if column.identity != "" {
		return column.identity
	}
	return "manual:" + column.Name
}

func sameColumnSet(a, b map[string]Column) bool {
	if len(a) != len(b) {
		return false
	}
	for name := range a {
		if _, ok := b[name]; !ok {
			return false
		}
	}
	return true
}

func columnOrder(columns []Column) []string {
	order := make([]string, len(columns))
	for i, column := range columns {
		order[i] = column.Name
	}
	return order
}

func diffElements(oldElements, newElements []SchemaElement) (added, removed []string) {
	added = []string{}
	removed = []string{}
	oldCounts := elementCounts(oldElements)
	newCounts := elementCounts(newElements)
	for canonical, newList := range newCounts {
		oldCount := len(oldCounts[canonical])
		for i := oldCount; i < len(newList); i++ {
			added = append(added, newList[i])
		}
	}
	for canonical, oldList := range oldCounts {
		newCount := len(newCounts[canonical])
		for i := newCount; i < len(oldList); i++ {
			removed = append(removed, oldList[i])
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func elementCounts(elements []SchemaElement) map[string][]string {
	counts := make(map[string][]string, len(elements))
	for _, element := range elements {
		counts[element.canonical] = append(counts[element.canonical], element.Definition)
	}
	for canonical := range counts {
		sort.Strings(counts[canonical])
	}
	return counts
}

func diffOptions(a, b ParsedTable) []OptionChange {
	oldOptions := optionMap(a.Options)
	newOptions := optionMap(b.Options)
	changes := make([]OptionChange, 0)
	if a.modifiers != b.modifiers {
		changes = append(changes, OptionChange{Name: "create-modifiers", Old: a.modifiers, New: b.modifiers})
	}
	keys := make(map[string]struct{}, len(oldOptions)+len(newOptions))
	for key := range oldOptions {
		keys[key] = struct{}{}
	}
	for key := range newOptions {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		old, oldOK := oldOptions[key]
		newOption, newOK := newOptions[key]
		if oldOK && newOK && old.canonical == newOption.canonical {
			continue
		}
		change := OptionChange{Name: key}
		if oldOK {
			change.Old = old.Value
		}
		if newOK {
			change.New = newOption.Value
		}
		changes = append(changes, change)
	}
	return changes
}

func optionMap(options []TableOption) map[string]TableOption {
	indexed := make(map[string]TableOption, len(options))
	for _, option := range options {
		indexed[option.Name] = option
	}
	return indexed
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
