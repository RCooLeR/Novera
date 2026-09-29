package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRawHelpersAreNotBridgeBound(t *testing.T) {
	typ := reflect.TypeOf(&Service{})
	for _, name := range []string{"ReadRaw", "WriteRaw", "ReadRawBounded"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Fatalf("internal helper %s must not be an exported Service method", name)
		}
	}
	bindingPath := filepath.Join("..", "..", "frontend", "bindings", "novera", "internal", "workspace", "service.ts")
	binding, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ReadRaw", "WriteRaw", "ReadRawBounded"} {
		if strings.Contains(string(binding), "function "+name) {
			t.Fatalf("generated Workspace binding exposes internal helper %s", name)
		}
	}
}
