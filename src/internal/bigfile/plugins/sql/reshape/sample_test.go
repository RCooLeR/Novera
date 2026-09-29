package reshape

import (
	"errors"
	"strings"
	"testing"
)

func TestSampleInsertRowsUsesStrictTupleGrammar(t *testing.T) {
	tests := []struct {
		name  string
		input string
		rows  int
		want  string
	}{
		{
			name:  "column list is not a row",
			input: "INSERT INTO t (a,b) VALUES (1,2),(3,4);",
			rows:  1,
			want:  "INSERT INTO t (a,b) VALUES (1,2);\n",
		},
		{
			name:  "nested expression parentheses stay in one row",
			input: "INSERT INTO t VALUES (COALESCE(NULL, 1),'x'),(2,'y');",
			rows:  1,
			want:  "INSERT INTO t VALUES (COALESCE(NULL, 1),'x');\n",
		},
		{
			name:  "comments and later statements are preserved until limit",
			input: "-- first\nINSERT INTO t VALUES (1);\n/* next */ INSERT INTO t VALUES (2),(3);",
			rows:  2,
			want:  "-- first\nINSERT INTO t VALUES (1);\n/* next */ INSERT INTO t VALUES (2);\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, rows, satisfied, err := SampleInsertRows([]byte(test.input), test.rows)
			if err != nil {
				t.Fatal(err)
			}
			if rows != test.rows || !satisfied || string(got) != test.want {
				t.Fatalf("sample = %q, rows=%d satisfied=%v; want %q", got, rows, satisfied, test.want)
			}
		})
	}
}

func TestSampleInsertRowsRejectsUnsafeOrIncompleteInsert(t *testing.T) {
	for _, input := range []string{
		"INSERT INTO t VALUES (1),(2) ON DUPLICATE KEY UPDATE v=VALUES(v);",
		"INSERT INTO t VALUES (1",
	} {
		_, _, _, err := SampleInsertRows([]byte(input), 1)
		if err == nil {
			t.Fatalf("unsafe input %q was accepted", input)
		}
		if strings.Contains(input, "VALUES (1") && !strings.Contains(input, "ON DUPLICATE") && !errors.Is(err, ErrIncompleteStatement) {
			t.Fatalf("incomplete error = %v, want ErrIncompleteStatement", err)
		}
	}
}

func TestSampleInsertRowsScansUnsafeRemainderAfterRowLimit(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  error
	}{
		{
			name:  "COPY raw payload",
			input: "INSERT INTO safe VALUES (1); COPY imported(value) FROM STDIN;\nINSERT INTO payload VALUES (2),(3);\n\\.\n",
			want:  ErrUnsupportedCompoundStatement,
		},
		{
			name:  "dollar quoted body",
			input: "INSERT INTO safe VALUES (1); DO $$ BEGIN; INSERT INTO payload VALUES (2),(3); END; $$;",
			want:  ErrUnsupportedCompoundStatement,
		},
		{
			name:  "later unsupported INSERT clause",
			input: "INSERT INTO safe VALUES (1); INSERT INTO payload VALUES (2),(3) ON DUPLICATE KEY UPDATE v=VALUES(v);",
			want:  ErrUnsupportedInsert,
		},
		{
			name: "SQLite dot command before compound body",
			input: "INSERT INTO safe VALUES (1);\n.read setup.sql\n" +
				"CREATE TRIGGER trg AFTER INSERT ON src BEGIN SELECT 1; INSERT INTO payload VALUES (2),(3); END;",
			want: ErrUnsupportedCompoundStatement,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sample, rows, satisfied, err := SampleInsertRows([]byte(test.input), 1)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if sample != nil || rows != 1 || satisfied {
				t.Fatalf("failed sample = %q, rows=%d, satisfied=%v; want nil, 1, false", sample, rows, satisfied)
			}
		})
	}
}

func TestSampleInsertRowsPreservesSerializedPayloadBytes(t *testing.T) {
	tupleA := `(1,'a:2:{s:4:"semi";s:4:"a;b;";s:4:"hint";s:6:"/*!x*/";}')`
	tupleB := `(2,'O:8:"stdClass":1:{s:5:"shape";s:3:"),(";}')`
	prefix := "INSERT INTO `wp_options` (`id`,`value`) VALUES "
	input := prefix + tupleA + "," + tupleB + ";"
	sample, rows, satisfied, err := SampleInsertRows([]byte(input), 1)
	if err != nil {
		t.Fatal(err)
	}
	want := prefix + tupleA + ";\n"
	if string(sample) != want || rows != 1 || !satisfied {
		t.Fatalf("sample = %q, rows=%d, satisfied=%v; want %q", sample, rows, satisfied, want)
	}
}
