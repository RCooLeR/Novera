package jsonsafe

import "testing"

type strictFixture struct {
	Name   string        `json:"name"`
	Nested nestedFixture `json:"nested"`
}

type nestedFixture struct {
	Enabled bool `json:"enabled"`
}

func TestUnmarshalAcceptsCanonicalDocument(t *testing.T) {
	var got strictFixture
	if err := Unmarshal([]byte(`{"name":"Novera","nested":{"enabled":true}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Novera" || !got.Nested.Enabled {
		t.Fatalf("decoded fixture = %#v", got)
	}
}

func TestUnmarshalPreservesLegacyNullMergeSemantics(t *testing.T) {
	got := strictFixture{
		Name:   "default",
		Nested: nestedFixture{Enabled: true},
	}
	if err := Unmarshal([]byte(`{"name":null,"nested":null}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "default" || !got.Nested.Enabled {
		t.Fatalf("decoded fixture = %#v; null fields must preserve existing defaults", got)
	}
}

func TestUnmarshalRejectsAmbiguousOrNonCanonicalDocuments(t *testing.T) {
	tests := map[string][]byte{
		"duplicate field":        []byte(`{"name":"one","name":"two","nested":{"enabled":true}}`),
		"duplicate nested field": []byte(`{"name":"one","nested":{"enabled":true,"enabled":false}}`),
		"case variant":           []byte(`{"Name":"one","nested":{"enabled":true}}`),
		"unknown field":          []byte(`{"name":"one","extra":1,"nested":{"enabled":true}}`),
		"trailing value":         []byte(`{"name":"one","nested":{"enabled":true}} {}`),
		"invalid UTF-8":          append([]byte(`{"name":"`), 0xff, '"', '}'),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			var got strictFixture
			if err := Unmarshal(input, &got); err == nil {
				t.Fatal("Unmarshal unexpectedly accepted unsafe JSON")
			}
		})
	}
}
