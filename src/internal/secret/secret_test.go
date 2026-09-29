package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func failingStorePath(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, "secrets.json")
}

func TestSetPersistenceFailureDoesNotMutateMemory(t *testing.T) {
	svc := &Store{
		path:  failingStorePath(t),
		data:  map[string]string{"existing": testCiphertext(1)},
		hooks: testStoreHooks(),
	}
	if err := svc.Set("new", "secret"); err == nil {
		t.Fatal("Set unexpectedly succeeded with an unwritable store path")
	}
	if _, ok := svc.data["new"]; ok {
		t.Fatal("failed Set leaked into in-memory state")
	}
	if got := svc.data["existing"]; got != testCiphertext(1) {
		t.Fatalf("existing value changed to %q", got)
	}
}

func TestDeletePersistenceFailureDoesNotMutateMemory(t *testing.T) {
	svc := &Store{
		path:  failingStorePath(t),
		data:  map[string]string{"existing": testCiphertext(1)},
		hooks: testStoreHooks(),
	}
	if err := svc.Delete("existing"); err == nil {
		t.Fatal("Delete unexpectedly succeeded with an unwritable store path")
	}
	if got := svc.data["existing"]; got != testCiphertext(1) {
		t.Fatalf("failed Delete changed in-memory value to %q", got)
	}
}

func TestEmptySetPersistenceFailureDoesNotMutateMemory(t *testing.T) {
	svc := &Store{
		path:  failingStorePath(t),
		data:  map[string]string{"existing": testCiphertext(1)},
		hooks: testStoreHooks(),
	}
	if err := svc.Set("existing", ""); err == nil {
		t.Fatal("empty Set unexpectedly succeeded with an unwritable store path")
	}
	if got := svc.data["existing"]; got != testCiphertext(1) {
		t.Fatalf("failed empty Set changed in-memory value to %q", got)
	}
}
