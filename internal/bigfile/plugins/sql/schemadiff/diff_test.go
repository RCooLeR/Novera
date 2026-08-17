package schemadiff

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiffOneDimensionMutationCorpus(t *testing.T) {
	base := "CREATE TABLE t (id int NOT NULL DEFAULT 1, name varchar(20), generated_col varchar(20) GENERATED ALWAYS AS (lower(name)) STORED, PRIMARY KEY (id), UNIQUE KEY uq_name (name)) ENGINE=InnoDB COLLATE=utf8mb4_bin;"
	cases := map[string]string{
		"type":                 strings.Replace(base, "id int", "id bigint", 1),
		"width":                strings.Replace(base, "varchar(20)", "varchar(21)", 1),
		"nullability":          strings.Replace(base, "id int NOT NULL", "id int NULL", 1),
		"default":              strings.Replace(base, "DEFAULT 1", "DEFAULT 2", 1),
		"generated expression": strings.Replace(base, "lower(name)", "upper(name)", 1),
		"column collation":     strings.Replace(base, "name varchar(20)", "name varchar(20) COLLATE utf8mb4_general_ci", 1),
		"primary key":          strings.Replace(base, "PRIMARY KEY (id)", "PRIMARY KEY (name)", 1),
		"unique index":         strings.Replace(base, "UNIQUE KEY uq_name (name)", "UNIQUE KEY uq_name (name,id)", 1),
		"engine":               strings.Replace(base, "ENGINE=InnoDB", "ENGINE=MyISAM", 1),
		"table collation":      strings.Replace(base, "utf8mb4_bin", "utf8mb4_general_ci", 1),
		"column clause order":  strings.Replace(base, "NOT NULL DEFAULT 1", "DEFAULT 1 NOT NULL", 1),
	}
	for name, changedDDL := range cases {
		name, changedDDL := name, changedDDL
		t.Run(name, func(t *testing.T) {
			result := Diff([]Table{parsedTable("t", base)}, []Table{parsedTable("t", changedDDL)})
			assertOneChangedTable(t, result)
			if result.ChangedTables[0].Status != DiffChanged {
				t.Fatalf("status = %q", result.ChangedTables[0].Status)
			}
		})
	}
}

func TestDiffDetectsConstraintIndexForeignKeyCheckAndCommentChanges(t *testing.T) {
	baseEntries := "id int, parent_id int, code varchar(20), PRIMARY KEY (id), UNIQUE KEY uq_code (code), CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parent(id) ON DELETE CASCADE, CHECK (id > 0)"
	cases := map[string]string{
		"constraint name": strings.Replace(baseEntries, "fk_parent", "fk_parent_2", 1),
		"foreign action":  strings.Replace(baseEntries, "ON DELETE CASCADE", "ON DELETE RESTRICT", 1),
		"check":           strings.Replace(baseEntries, "id > 0", "id >= 0", 1),
		"index order":     strings.Replace(baseEntries, "uq_code (code)", "uq_code (code,id)", 1),
	}
	for name, entries := range cases {
		name, entries := name, entries
		t.Run(name, func(t *testing.T) {
			oldDDL := "CREATE TABLE t (" + baseEntries + ") COMMENT='ABC';"
			newDDL := "CREATE TABLE t (" + entries + ") COMMENT='ABC';"
			result := Diff([]Table{parsedTable("t", oldDDL)}, []Table{parsedTable("t", newDDL)})
			assertOneChangedTable(t, result)
			change := result.ChangedTables[0]
			if len(change.AddedConstraints)+len(change.RemovedConstraints)+len(change.AddedIndexes)+len(change.RemovedIndexes) == 0 {
				t.Fatalf("structural change lacks constraint/index evidence: %#v", change)
			}
		})
	}

	commentCase := Diff(
		[]Table{parsedTable("t", "CREATE TABLE t (id int) COMMENT='ABC';")},
		[]Table{parsedTable("t", "CREATE TABLE t (id int) COMMENT='abc';")},
	)
	assertOneChangedTable(t, commentCase)
	if len(commentCase.ChangedTables[0].ChangedOptions) != 1 {
		t.Fatalf("table COMMENT change = %#v", commentCase)
	}
}

func TestDiffTableOptionMutationCorpus(t *testing.T) {
	base := "CREATE TABLE t (id int) ENGINE=InnoDB CHARSET=utf8mb4 COLLATE=utf8mb4_bin ROW_FORMAT=DYNAMIC COMMENT='A' PARTITION BY HASH(id) PARTITIONS 2;"
	cases := map[string]string{
		"charset":    strings.Replace(base, "CHARSET=utf8mb4", "CHARSET=latin1", 1),
		"collation":  strings.Replace(base, "utf8mb4_bin", "utf8mb4_general_ci", 1),
		"row format": strings.Replace(base, "ROW_FORMAT=DYNAMIC", "ROW_FORMAT=COMPRESSED", 1),
		"comment":    strings.Replace(base, "COMMENT='A'", "COMMENT='a'", 1),
		"partition":  strings.Replace(base, "PARTITIONS 2", "PARTITIONS 4", 1),
	}
	for name, changed := range cases {
		name, changed := name, changed
		t.Run(name, func(t *testing.T) {
			result := Diff([]Table{parsedTable("t", base)}, []Table{parsedTable("t", changed)})
			assertOneChangedTable(t, result)
			if len(result.ChangedTables[0].ChangedOptions) == 0 {
				t.Fatalf("option change lacks evidence: %#v", result)
			}
		})
	}

	strict := Diff(
		[]Table{parsedTable("t", "CREATE TABLE t (id int);")},
		[]Table{parsedTable("t", "CREATE TABLE t (id int) STRICT;")},
	)
	assertOneChangedTable(t, strict)
	if len(strict.ChangedTables[0].ChangedOptions) != 1 {
		t.Fatalf("STRICT option change = %#v", strict)
	}

	tablespace := Diff(
		[]Table{parsedTable("t", "CREATE TABLE t (id int) TABLESPACE old_space;")},
		[]Table{parsedTable("t", "CREATE TABLE t (id int) TABLESPACE new_space;")},
	)
	assertOneChangedTable(t, tablespace)
}

func TestDiffDetectsColumnOrderButIgnoresConstraintAndOptionOrder(t *testing.T) {
	oldDDL := "CREATE TABLE t (a int, b int, PRIMARY KEY(a), UNIQUE KEY uq_b(b)) ENGINE=InnoDB CHARSET=utf8mb4;"
	reorderedColumns := "CREATE TABLE t (b int, a int, PRIMARY KEY(a), UNIQUE KEY uq_b(b)) ENGINE=InnoDB CHARSET=utf8mb4;"
	result := Diff([]Table{parsedTable("t", oldDDL)}, []Table{parsedTable("t", reorderedColumns)})
	assertOneChangedTable(t, result)
	change := result.ChangedTables[0]
	if !change.ColumnOrderChanged || strings.Join(change.OldColumnOrder, ",") != "a,b" || strings.Join(change.NewColumnOrder, ",") != "b,a" {
		t.Fatalf("order change = %#v", change)
	}

	cosmeticReorder := "CREATE TABLE t (a int, b int, UNIQUE KEY uq_b(b), PRIMARY KEY(a)) CHARSET=utf8mb4 ENGINE=InnoDB;"
	equal := Diff([]Table{parsedTable("t", oldDDL)}, []Table{parsedTable("t", cosmeticReorder)})
	if equal.UnchangedCount != 1 || len(equal.ChangedTables) != 0 {
		t.Fatalf("constraint/option reorder should be cosmetic: %#v", equal)
	}
}

func TestDiffIdentifierPolicyIsExactCaseAndDecodedQuoteAware(t *testing.T) {
	quotedVsBare := Diff(
		[]Table{parsedTable("t", "CREATE TABLE t (`id` int);")},
		[]Table{parsedTable("t", "CREATE TABLE t (id int);")},
	)
	if quotedVsBare.UnchangedCount != 1 {
		t.Fatalf("decoded quote wrappers changed declared column identity: %#v", quotedVsBare)
	}
	mixedQuoted := Diff(
		[]Table{parsedTable("t", "CREATE TABLE t (MixedCase int);")},
		[]Table{parsedTable("t", "CREATE TABLE t (\"MixedCase\" int);")},
	)
	assertOneChangedTable(t, mixedQuoted)
	if len(mixedQuoted.ChangedTables[0].AddedColumns) != 1 || len(mixedQuoted.ChangedTables[0].RemovedColumns) != 1 {
		t.Fatalf("mixed-case quote policy failed closed: %#v", mixedQuoted)
	}

	columnCase := Diff(
		[]Table{parsedTable("t", "CREATE TABLE t (ID int);")},
		[]Table{parsedTable("t", "CREATE TABLE t (id int);")},
	)
	assertOneChangedTable(t, columnCase)
	if len(columnCase.ChangedTables[0].AddedColumns) != 1 || len(columnCase.ChangedTables[0].RemovedColumns) != 1 {
		t.Fatalf("case-sensitive column identity = %#v", columnCase)
	}

	tableCase := Diff(
		[]Table{parsedTable("Users", "CREATE TABLE Users (id int);")},
		[]Table{parsedTable("users", "CREATE TABLE users (id int);")},
	)
	if len(tableCase.AddedTables) != 1 || len(tableCase.RemovedTables) != 1 || tableCase.UnchangedCount != 0 {
		t.Fatalf("case-sensitive table identity = %#v", tableCase)
	}
}

func TestDiffQualifiedIdentityAndMultiDatabaseSchemas(t *testing.T) {
	oldTables := []Table{
		parsedTable("db1.users", "CREATE TABLE db1.users (id int);"),
		parsedTable("db2.users", "CREATE TABLE db2.users (id int);"),
	}
	newTables := []Table{
		parsedTable("db1.users", "CREATE TABLE db1.users (id int);"),
		parsedTable("db2.users", "CREATE TABLE db2.users (id bigint);"),
	}
	result := Diff(oldTables, newTables)
	if result.UnchangedCount != 1 || len(result.ChangedTables) != 1 || result.ChangedTables[0].Name != "db2.users" {
		t.Fatalf("multi-database diff = %#v", result)
	}

	identity := Diff(
		[]Table{parsedTable("users", "CREATE TABLE db1.users (id int);")},
		[]Table{parsedTable("users", "CREATE TABLE db2.users (id int);")},
	)
	assertOneChangedTable(t, identity)
	if identity.ChangedTables[0].IdentityChanged == nil {
		t.Fatalf("qualified identity change missed: %#v", identity)
	}
}

func TestDiffAddedRemovedAndChangedTablesRemainStable(t *testing.T) {
	oldTables := []Table{
		parsedTable("z_removed", "CREATE TABLE z_removed (id int);"),
		parsedTable("shared", "CREATE TABLE shared (id int);"),
	}
	newTables := []Table{
		parsedTable("a_added", "CREATE TABLE a_added (id int);"),
		parsedTable("shared", "CREATE TABLE shared (id bigint);"),
	}
	result := Diff(oldTables, newTables)
	if strings.Join(result.AddedTables, ",") != "a_added" || strings.Join(result.RemovedTables, ",") != "z_removed" ||
		len(result.ChangedTables) != 1 || result.ChangedTables[0].Name != "shared" {
		t.Fatalf("mixed table diff = %#v", result)
	}
}

func TestDiffFailsClosedForUnknownLegacyDuplicateAndOverLimitInputs(t *testing.T) {
	complete := parsedTable("t", "CREATE TABLE t (id int);")
	unknown := Table{Name: "t"}
	for _, tc := range []struct {
		name string
		old  []Table
		new  []Table
	}{
		{name: "both unknown", old: []Table{unknown}, new: []Table{unknown}},
		{name: "old unknown", old: []Table{unknown}, new: []Table{complete}},
		{name: "new unknown", old: []Table{complete}, new: []Table{unknown}},
		{name: "legacy columns", old: []Table{{Name: "t", Columns: []Column{{Name: "id", Definition: "int"}}}}, new: []Table{{Name: "t", Columns: []Column{{Name: "id", Definition: "int"}}}}},
		{name: "duplicate old", old: []Table{complete, complete}, new: []Table{complete}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			result := Diff(tc.old, tc.new)
			assertOneChangedTable(t, result)
			if result.ChangedTables[0].Status != DiffUnknown || result.ChangedTables[0].Reason == "" || result.UnchangedCount != 0 {
				t.Fatalf("fail-closed result = %#v", result)
			}
		})
	}

	overLimit := make([]Table, MaxDiffTableCount+1)
	result := Diff(overLimit, nil)
	assertOneChangedTable(t, result)
	if result.ChangedTables[0].Status != DiffUnknown || !strings.Contains(result.ChangedTables[0].Reason, "table count") {
		t.Fatalf("table-count result = %#v", result)
	}

	forged := Table{Name: "t", Definition: ParsedTable{Status: ParseComplete, Items: []Column{{Name: "id", Definition: "int"}}}}
	result = Diff([]Table{forged}, []Table{forged})
	assertOneChangedTable(t, result)
	if result.ChangedTables[0].Status != DiffUnknown {
		t.Fatalf("forged complete parse was trusted: %#v", result)
	}

	mutatedDefinition := ParseTable([]byte("CREATE TABLE t (id int);"))
	mutatedDefinition.Items[0].Definition = "bigint"
	mutated := Table{Name: "t", Definition: mutatedDefinition}
	result = Diff([]Table{mutated}, []Table{mutated})
	assertOneChangedTable(t, result)
	if result.ChangedTables[0].Status != DiffUnknown {
		t.Fatalf("mutated parsed definition was trusted: %#v", result)
	}
}

func TestDiffNilAndEmptyResultsAreStableNonNilJSON(t *testing.T) {
	for _, result := range []Result{Diff(nil, nil), Diff([]Table{}, []Table{})} {
		if result.AddedTables == nil || result.RemovedTables == nil || result.ChangedTables == nil || result.UnchangedCount != 0 {
			t.Fatalf("empty result = %#v", result)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		const want = `{"addedTables":[],"removedTables":[],"changedTables":[],"unchangedCount":0}`
		if string(encoded) != want {
			t.Fatalf("empty JSON = %s, want %s", encoded, want)
		}
	}
}

func TestDiffByteBudgetAccountingIsBounded(t *testing.T) {
	table := Table{Name: "t", Definition: ParseTable([]byte("CREATE TABLE t (id varchar(10));"))}
	if !tableSetsWithinByteBudget([]Table{table}, nil, 1024) {
		t.Fatal("small parsed table unexpectedly exceeded a 1 KiB test budget")
	}
	if tableSetsWithinByteBudget([]Table{table}, nil, 1) {
		t.Fatal("one-byte test budget unexpectedly accepted a parsed table")
	}
}

func TestDiffGoldenChangedPayloadIsDeterministic(t *testing.T) {
	oldDDL := "CREATE TABLE t (id int, name varchar(10), PRIMARY KEY(id)) ENGINE=InnoDB;"
	newDDL := "CREATE TABLE t (name varchar(20), id int, UNIQUE KEY uq_name(name)) ENGINE=MyISAM;"
	first := Diff([]Table{parsedTable("t", oldDDL)}, []Table{parsedTable("t", newDDL)})
	second := Diff([]Table{parsedTable("t", oldDDL)}, []Table{parsedTable("t", newDDL)})
	a, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("unstable JSON:\n%s\n%s", a, b)
	}
	change := first.ChangedTables[0]
	if change.Status != DiffChanged || !change.ColumnOrderChanged || len(change.ChangedColumns) != 1 ||
		len(change.AddedIndexes) != 1 || len(change.RemovedConstraints) != 1 || len(change.ChangedOptions) != 1 {
		t.Fatalf("golden semantic dimensions = %#v", change)
	}
}

func assertOneChangedTable(t *testing.T, result Result) {
	t.Helper()
	if result.UnchangedCount != 0 || len(result.ChangedTables) != 1 || len(result.AddedTables) != 0 || len(result.RemovedTables) != 0 {
		t.Fatalf("result = %#v, want exactly one changed table", result)
	}
}
