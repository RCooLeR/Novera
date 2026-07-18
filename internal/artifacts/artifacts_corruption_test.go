package artifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func artifactRegistryPath(root string) string {
	return filepath.Join(root, ".novera", "artifacts.json")
}

func sameArtifactEvidencePath(a, b string) bool {
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return sameRegistryPath(resolvedA, resolvedB)
	}
	return sameRegistryPath(a, b)
}

func writeArtifactRegistryEvidence(t *testing.T, root string, evidence []byte) string {
	t.Helper()
	path := artifactRegistryPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func versionedRegistryBytes(t *testing.T, list []Artifact) []byte {
	t.Helper()
	b, err := json.MarshalIndent(registryEnvelope{Version: registryVersion, Artifacts: list}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCorruptRegistryFailsClosedAndPreservesExactEvidence(t *testing.T) {
	cases := map[string][]byte{
		"malformed":           []byte(`not-json-at-all`),
		"truncated":           []byte(`{"version":1,"artifacts":[{"id":"old"`),
		"wrong version":       []byte(`{"version":99,"artifacts":[]}`),
		"missing array":       []byte(`{"version":1}`),
		"null array":          []byte(`{"version":1,"artifacts":null}`),
		"unknown envelope":    []byte(`{"version":1,"artifacts":[],"future":true}`),
		"duplicate version":   []byte(`{"version":1,"version":1,"artifacts":[]}`),
		"duplicate artifacts": []byte(`{"version":1,"artifacts":[],"artifacts":[]}`),
		"duplicate nested id": []byte(`{"version":1,"artifacts":[{"id":"first","ID":"second","path":"old.txt"}]}`),
		"invalid UTF-8":       []byte{'{', '"', 'v', 'e', 'r', 's', 'i', 'o', 'n', '"', ':', '1', ',', '"', 'a', 'r', 't', 'i', 'f', 'a', 'c', 't', 's', '"', ':', '[', '{', '"', 'i', 'd', '"', ':', '"', 0xff, '"', '}', ']', '}'},
	}
	for name, evidence := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("content"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := writeArtifactRegistryEvidence(t, root, evidence)
			s := New(fakeWS{root: root})

			_, err := s.ListArtifacts()
			if !errors.Is(err, ErrRegistryCorrupt) {
				t.Fatalf("ListArtifacts error = %v, want ErrRegistryCorrupt", err)
			}
			var corrupt *RegistryCorruptionError
			if !errors.As(err, &corrupt) || !sameArtifactEvidencePath(corrupt.Path, path) {
				t.Fatalf("corruption error = %#v, want path %q", corrupt, path)
			}
			if !strings.Contains(err.Error(), "mutations are blocked") || !strings.Contains(err.Error(), "restore deliberately") {
				t.Fatalf("diagnostic is not actionable: %v", err)
			}

			if _, err := s.CreateArtifact(Artifact{Path: "new.txt"}); !errors.Is(err, ErrRegistryCorrupt) {
				t.Fatalf("CreateArtifact error = %v, want ErrRegistryCorrupt", err)
			}
			if err := s.SetArchived("old", true); !errors.Is(err, ErrRegistryCorrupt) {
				t.Fatalf("SetArchived error = %v, want ErrRegistryCorrupt", err)
			}
			if err := s.DeleteArtifact("old"); !errors.Is(err, ErrRegistryCorrupt) {
				t.Fatalf("DeleteArtifact error = %v, want ErrRegistryCorrupt", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, evidence) {
				t.Fatalf("registry evidence changed:\n got %q\nwant %q", got, evidence)
			}
			if _, err := os.Stat(path + registryLastGoodSuffix); !os.IsNotExist(err) {
				t.Fatalf("mutation created/replaced recovery data after corruption: %v", err)
			}
		})
	}
}

func TestCreateRejectsStringsThatCannotRoundTripBeforePublishing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "content.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(fakeWS{root: root})
	for _, tt := range []struct {
		name  string
		value Artifact
	}{
		{
			name:  "invalid UTF-8 title",
			value: Artifact{Path: "content.txt", Title: string([]byte{0xff})},
		},
		{
			name:  "oversized note",
			value: Artifact{Path: "content.txt", Note: strings.Repeat("n", maxArtifactNoteBytes+1)},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.CreateArtifact(tt.value); err == nil {
				t.Fatal("CreateArtifact accepted an unsafe field")
			}
			if _, err := os.Stat(filepath.Join(root, ".novera", "artifacts.json")); !os.IsNotExist(err) {
				t.Fatalf("unsafe artifact registry was published: %v", err)
			}
		})
	}
}

func TestLinkedRegistryPrimaryIsRejectedWithoutReadingOutsideEvidence(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, ".novera")
	if err := os.MkdirAll(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	outsideBytes := []byte(`{"version":1,"artifacts":[]}`)
	if err := os.WriteFile(outside, outsideBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(managed, "artifacts.json")
	if err := os.Symlink(outside, primary); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	s := New(fakeWS{root: root})
	if _, err := s.ListArtifacts(); !errors.Is(err, ErrRegistryUnsafePath) || !errors.Is(err, ErrRegistryCorrupt) {
		t.Fatalf("ListArtifacts linked-primary error = %v, want unsafe/corrupt classification", err)
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, outsideBytes) {
		t.Fatal("outside registry target was modified")
	}
}

func TestRegistryRejectsLinkedMetadataDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifact.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".novera")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}

	s := New(fakeWS{root: root})
	if _, err := s.CreateArtifact(Artifact{Path: "artifact.txt"}); !errors.Is(err, ErrRegistryUnsafePath) {
		t.Fatalf("CreateArtifact error = %v, want ErrRegistryUnsafePath", err)
	}
	for _, name := range []string{"artifacts.json", "artifacts.json" + registryLastGoodSuffix} {
		if _, err := os.Stat(filepath.Join(outside, name)); !os.IsNotExist(err) {
			t.Fatalf("registry escaped through linked directory to %s: %v", name, err)
		}
	}
}

func TestMissingPrimaryWithRecoveryCopyRequiresDeliberateRestore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	primary := artifactRegistryPath(root)
	if err := os.MkdirAll(filepath.Dir(primary), 0o700); err != nil {
		t.Fatal(err)
	}
	recovery := versionedRegistryBytes(t, []Artifact{{ID: "old", Path: "old.txt"}})
	if err := os.WriteFile(primary+registryLastGoodSuffix, recovery, 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(fakeWS{root: root})
	_, err := s.ListArtifacts()
	var corrupt *RegistryCorruptionError
	if !errors.As(err, &corrupt) || !sameArtifactEvidencePath(corrupt.LastKnownGoodPath, primary+registryLastGoodSuffix) {
		t.Fatalf("error = %#v, want verified recovery path", corrupt)
	}
	if _, err := s.CreateArtifact(Artifact{Path: "new.txt"}); !errors.Is(err, ErrRegistryCorrupt) {
		t.Fatalf("CreateArtifact error = %v, want recovery-required error", err)
	}
	got, err := os.ReadFile(primary + registryLastGoodSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, recovery) {
		t.Fatal("recovery copy changed without a deliberate restore")
	}
	if _, err := os.Stat(primary); !os.IsNotExist(err) {
		t.Fatalf("missing primary was silently recreated: %v", err)
	}
}

func TestMissingPrimaryWithCorruptRecoveryCopyIsNotTreatedAsFresh(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	primary := artifactRegistryPath(root)
	if err := os.MkdirAll(filepath.Dir(primary), 0o700); err != nil {
		t.Fatal(err)
	}
	evidence := []byte(`{"version":1`)
	if err := os.WriteFile(primary+registryLastGoodSuffix, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(fakeWS{root: root})
	if _, err := s.CreateArtifact(Artifact{Path: "new.txt"}); !errors.Is(err, ErrRegistryCorrupt) {
		t.Fatalf("CreateArtifact error = %v, want ErrRegistryCorrupt", err)
	}
	got, err := os.ReadFile(primary + registryLastGoodSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, evidence) {
		t.Fatal("corrupt recovery evidence was overwritten")
	}
}

func TestLegacyRegistryMigratesOnlyAfterSuccessfulMutation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"old.txt", "new.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	legacy := []byte("[\n  {\"id\":\"old\",\"kind\":\"file\",\"title\":\"Old\",\"path\":\"old.txt\",\"tool\":\"legacy\",\"note\":\"\",\"sources\":[],\"createdAt\":1,\"updatedAt\":1,\"archived\":false,\"stale\":false,\"missing\":false}\n]\n")
	primary := writeArtifactRegistryEvidence(t, root, legacy)
	s := New(fakeWS{root: root})
	if _, err := s.CreateArtifact(Artifact{Path: "new.txt"}); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(primary + registryLastGoodSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, legacy) {
		t.Fatalf("last-known-good copy is not the exact legacy input:\n got %q\nwant %q", backup, legacy)
	}
	current, err := os.ReadFile(primary)
	if err != nil {
		t.Fatal(err)
	}
	list, err := parseRegistry(current)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || bytes.HasPrefix(bytes.TrimSpace(current), []byte("[")) {
		t.Fatalf("registry was not migrated to versioned form: count=%d body=%s", len(list), current)
	}
}

func TestPrimaryPublishFaultPreservesPrimaryAndLastKnownGood(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	primaryBytes := versionedRegistryBytes(t, []Artifact{{ID: "old", Path: "old.txt"}})
	primary := writeArtifactRegistryEvidence(t, root, primaryBytes)
	s := New(fakeWS{root: root})
	wantErr := errors.New("injected primary publish failure")
	s.writeRegistry = func(path string, data []byte) error {
		if strings.HasSuffix(path, registryLastGoodSuffix) {
			return atomicWriteRegistry(path, data)
		}
		return wantErr
	}
	if err := s.SetArchived("old", true); !errors.Is(err, wantErr) {
		t.Fatalf("SetArchived error = %v, want injected error", err)
	}
	for _, path := range []string{primary, primary + registryLastGoodSuffix} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, primaryBytes) {
			t.Fatalf("%s changed after failed publish", path)
		}
	}
}

func TestExternalChangeDuringBackupIsNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	primaryBytes := versionedRegistryBytes(t, []Artifact{{ID: "old", Path: "old.txt"}})
	primary := writeArtifactRegistryEvidence(t, root, primaryBytes)
	evidence := []byte(`{"version":1,"artifacts":`)
	s := New(fakeWS{root: root})
	s.writeRegistry = func(path string, data []byte) error {
		if !strings.HasSuffix(path, registryLastGoodSuffix) {
			t.Fatal("primary writer ran after concurrent corruption was detected")
		}
		if err := atomicWriteRegistry(path, data); err != nil {
			return err
		}
		return os.WriteFile(primary, evidence, 0o600)
	}
	if err := s.SetArchived("old", true); !errors.Is(err, ErrRegistryConcurrentChange) {
		t.Fatalf("SetArchived error = %v, want ErrRegistryConcurrentChange", err)
	}
	got, err := os.ReadFile(primary)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, evidence) {
		t.Fatal("concurrent corrupt evidence was overwritten")
	}
}

func TestConcurrentArtifactCreatesRemainSerializable(t *testing.T) {
	root := t.TempDir()
	const count = 24
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("artifact-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := New(fakeWS{root: root})
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("artifact-%02d.txt", i)
			_, err := s.CreateArtifact(Artifact{Path: name})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("CreateArtifact: %v", err)
		}
	}
	list, err := s.ListArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != count {
		t.Fatalf("artifact count = %d, want %d", len(list), count)
	}
}
