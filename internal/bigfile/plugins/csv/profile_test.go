package csv

import (
	"context"
	"strings"
	"testing"
)

func TestProfileColumns(t *testing.T) {
	in := "id,city,age\n1,NYC,30\n2,NYC,25\n3,LA,\n4,NYC,40\n"
	rep, err := ProfileColumns(context.Background(), strings.NewReader(in), SchemaOptions{
		Delimiter:  ',',
		HasHeader:  true,
		NullValues: []string{""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.RecordsScanned != 5 || len(rep.Columns) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	city := rep.Columns[1]
	if city.Name != "city" || city.Distinct != 2 {
		t.Fatalf("city = %+v, want 2 distinct", city)
	}
	if len(city.Top) == 0 || city.Top[0].Value != "NYC" || city.Top[0].Count != 3 {
		t.Fatalf("city top = %+v, want NYC=3 first", city.Top)
	}
	age := rep.Columns[2]
	if age.Null != 1 || age.NonNull != 3 {
		t.Fatalf("age null/nonnull = %d/%d, want 1/3", age.Null, age.NonNull)
	}
	if age.Min != "25" || age.Max != "40" {
		t.Fatalf("age min/max = %q/%q, want 25/40", age.Min, age.Max)
	}
}

func TestProfileColumnsRaggedRows(t *testing.T) {
	in := "a,b,c\n1,2,3\n4,5\n6,7,8,9\n"
	rep, err := ProfileColumns(context.Background(), strings.NewReader(in), SchemaOptions{Delimiter: ',', HasHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.RaggedRows != 2 {
		t.Fatalf("ragged = %d, want 2", rep.RaggedRows)
	}
}
