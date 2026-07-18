package secret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCorruptEncryptedStoreIsPreservedAndMutationBlocked(t *testing.T) {
	for name, evidence := range map[string][]byte{
		"malformed": []byte(`{"owned.ref":"truncated"`),
		"null":      []byte(`null`),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secrets.json")
			if err := os.WriteFile(path, evidence, 0o600); err != nil {
				t.Fatal(err)
			}
			s := &Store{path: path, data: map[string]string{}}
			s.load()

			err := s.Health()
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Health error = %v, want ErrStoreCorrupt", err)
			}
			var corrupt *StoreCorruptionError
			if !errors.As(err, &corrupt) || corrupt.Path != path {
				t.Fatalf("corruption diagnostic = %#v, want path %q", corrupt, path)
			}
			if !strings.Contains(err.Error(), "left unchanged") || !strings.Contains(err.Error(), "restore or inspect") {
				t.Fatalf("diagnostic is not actionable: %v", err)
			}
			if _, ok := s.Get("owned.ref"); ok {
				t.Fatal("Get succeeded against corrupt store")
			}
			if err := s.Set("owned.ref", "replacement"); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Set error = %v, want ErrStoreCorrupt", err)
			}
			if err := s.Delete("owned.ref"); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("Delete error = %v, want ErrStoreCorrupt", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, evidence) {
				t.Fatalf("corrupt evidence changed:\n got %q\nwant %q", got, evidence)
			}
			if _, err := os.Stat(path + ".corrupt"); !os.IsNotExist(err) {
				t.Fatalf("store was moved aside instead of preserved in place: %v", err)
			}
		})
	}
}

func TestCorruptEncryptedStoreRemainsFailClosedUnderConcurrency(t *testing.T) {
	evidence := []byte(`{"db.cred.v1.example":`)
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{path: path, data: map[string]string{}}
	s.load()

	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers*3)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Health()
			errs <- s.Set("llm.apikey.v1.example", "new-value")
			errs <- s.Delete("db.cred.v1.example")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrStoreCorrupt) {
			t.Errorf("concurrent operation error = %v, want ErrStoreCorrupt", err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, evidence) {
		t.Fatalf("concurrent operations changed evidence: got %q want %q", got, evidence)
	}
}
