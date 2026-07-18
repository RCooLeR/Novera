package secret

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testCiphertext(seed byte) string {
	raw := bytes.Repeat([]byte{seed}, minSecretCiphertextRawBytes+4)
	return base64.StdEncoding.EncodeToString(raw)
}

func testBoundCiphertext(ref, value string) string {
	refHash := sha256.Sum256([]byte(ref))
	raw := make([]byte, 0, len(refHash)+len(value))
	raw = append(raw, refHash[:]...)
	raw = append(raw, value...)
	return encodeCiphertextRecord(raw, ciphertextV2)
}

func testStoreHooks() storeHooks {
	return storeHooks{
		decryptSession: func(bool) (ciphertextDecryptor, error) {
			return func(record ciphertextRecord, ref string) (string, error) {
				if record.format == ciphertextV2 {
					refHash := sha256.Sum256([]byte(ref))
					if len(record.raw) < len(refHash) || !bytes.Equal(record.raw[:len(refHash)], refHash[:]) {
						return "", fmt.Errorf("%w: test ref binding mismatch", ErrCiphertextAuthentication)
					}
					return string(record.raw[len(refHash):]), nil
				}
				return "decrypted", nil
			}, nil
		},
		encrypt: func(value, ref string, _ bool) (string, error) {
			return testBoundCiphertext(ref, value), nil
		},
	}
}

func writeSecretEvidence(t *testing.T, path string, data map[string]string) []byte {
	t.Helper()
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

func assertCorruptionPreserved(t *testing.T, s *Store, evidence []byte) {
	t.Helper()
	if err := s.Health(); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Health error = %v, want ErrStoreCorrupt", err)
	}
	if _, _, err := s.GetChecked("missing"); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("GetChecked error = %v, want ErrStoreCorrupt", err)
	}
	if err := s.Set("replacement", "new secret"); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Set error = %v, want ErrStoreCorrupt", err)
	}
	if err := s.Replace("owned.ref", "replacement", "new secret"); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Replace error = %v, want ErrStoreCorrupt", err)
	}
	if err := s.Delete("missing"); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Delete error = %v, want ErrStoreCorrupt", err)
	}
	got, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, evidence) {
		t.Fatalf("corruption evidence changed: got %d bytes, want %d", len(got), len(evidence))
	}
}

func TestGetCheckedDistinguishesMissingFromFailureAndValidatesAllCiphertexts(t *testing.T) {
	first := testCiphertext(1)
	second := testCiphertext(2)
	decryptions := 0
	sessions := 0
	s := &Store{
		path: filepath.Join(t.TempDir(), "secrets.json"),
		data: map[string]string{"a": first, "b": second},
		hooks: storeHooks{
			decryptSession: func(bool) (ciphertextDecryptor, error) {
				sessions++
				return func(record ciphertextRecord, _ string) (string, error) {
					decryptions++
					switch encodeCiphertextRecord(record.raw, record.format) {
					case first:
						return "first plaintext", nil
					case second:
						return "second plaintext", nil
					default:
						return "", errors.New("unexpected ciphertext")
					}
				}, nil
			},
		},
	}
	got, found, err := s.GetChecked("b")
	if err != nil || !found || got != "second plaintext" {
		t.Fatalf("GetChecked = (%q, %v, %v), want second plaintext, true, nil", got, found, err)
	}
	if decryptions != 2 {
		t.Fatalf("GetChecked decrypted %d values, want every ciphertext (2)", decryptions)
	}
	if sessions != 1 {
		t.Fatalf("GetChecked created %d decrypt sessions, want one immutable backend snapshot", sessions)
	}
	got, found, err = s.GetChecked("missing")
	if err != nil || found || got != "" {
		t.Fatalf("missing GetChecked = (%q, %v, %v), want empty, false, nil", got, found, err)
	}
}

func TestGetManyCheckedUsesOneSnapshotAndReturnsOnlyRequestedRefs(t *testing.T) {
	first := testCiphertext(1)
	second := testCiphertext(2)
	third := testCiphertext(3)
	sessions := 0
	decryptions := 0
	plaintext := map[string]string{first: "one", second: "two", third: "three"}
	s := &Store{
		path: filepath.Join(t.TempDir(), "secrets.json"),
		data: map[string]string{"first": first, "second": second, "third": third},
		hooks: storeHooks{decryptSession: func(bool) (ciphertextDecryptor, error) {
			sessions++
			return func(record ciphertextRecord, _ string) (string, error) {
				decryptions++
				return plaintext[encodeCiphertextRecord(record.raw, record.format, record.keyID)], nil
			}, nil
		}},
	}
	got, err := s.GetManyChecked([]string{"third", "missing", "first", "first"})
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || decryptions != 3 {
		t.Fatalf("bulk lookup used %d sessions and %d decryptions, want 1 and 3", sessions, decryptions)
	}
	if len(got) != 2 || got["first"] != "one" || got["third"] != "three" {
		t.Fatalf("bulk lookup result = %#v", got)
	}
	if _, found := got["missing"]; found {
		t.Fatal("bulk lookup returned a missing ref")
	}
}

func TestGetManyCheckedAllowsConsumerLookupSetsLargerThanStoreCapacity(t *testing.T) {
	refs := make([]string, maxSecretEntries+1)
	for i := range refs {
		refs[i] = fmt.Sprintf("db.cred.v1.absent-%d", i)
	}
	s := &Store{
		path: filepath.Join(t.TempDir(), "secrets.json"),
		data: map[string]string{},
		hooks: storeHooks{decryptSession: func(bool) (ciphertextDecryptor, error) {
			return func(ciphertextRecord, string) (string, error) {
				return "", errors.New("empty store unexpectedly attempted decryption")
			}, nil
		}},
	}
	got, err := s.GetManyChecked(refs)
	if err != nil {
		t.Fatalf("lookup of %d absent consumer refs failed: %v", len(refs), err)
	}
	if len(got) != 0 {
		t.Fatalf("absent lookup returned values: %#v", got)
	}
}

func TestCiphertextShapeCorruptionIsLatchedAndPreserved(t *testing.T) {
	for name, ciphertext := range map[string]string{
		"malformed-base64": "not!base64",
		"truncated":        base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, minSecretCiphertextRawBytes-1)),
		"unknown-version":  ciphertextRecordPrefix + "v99:" + testCiphertext(1),
		"embedded-newline": testCiphertext(1) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secrets.json")
			evidence := []byte(fmt.Sprintf(`{"owned.ref":%q}`, ciphertext))
			if err := os.WriteFile(path, evidence, 0o600); err != nil {
				t.Fatal(err)
			}
			s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
			s.load()
			assertCorruptionPreserved(t, s, evidence)
		})
	}
}

func TestAuthenticationFailureIsLatchedWithoutLeakingSecretMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	ciphertext := testCiphertext(9)
	evidence := writeSecretEvidence(t, path, map[string]string{"owned.ref": ciphertext})
	s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
	s.hooks.decryptSession = func(bool) (ciphertextDecryptor, error) {
		return func(ciphertextRecord, string) (string, error) {
			return "", fmt.Errorf("%w: authentication tag mismatch", ErrCiphertextAuthentication)
		}, nil
	}
	s.load()
	err := s.Health()
	if !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Health error = %v, want ErrStoreCorrupt", err)
	}
	if strings.Contains(err.Error(), ciphertext) || strings.Contains(err.Error(), "super-secret-plaintext") {
		t.Fatalf("corruption diagnostic leaked secret material: %v", err)
	}

	// The failure remains latched even if a later backend call would succeed.
	s.hooks.decryptSession = func(bool) (ciphertextDecryptor, error) {
		return func(ciphertextRecord, string) (string, error) { return "super-secret-plaintext", nil }, nil
	}
	assertCorruptionPreserved(t, s, evidence)
}

func TestMutationValidatesAndLatchesAuthenticationFailureWithoutPriorHealthCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	ciphertext := testCiphertext(7)
	evidence := writeSecretEvidence(t, path, map[string]string{"owned.ref": ciphertext})
	s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
	s.hooks.decryptSession = func(bool) (ciphertextDecryptor, error) {
		return func(ciphertextRecord, string) (string, error) {
			return "", fmt.Errorf("%w: simulated platform decryption failure", ErrCiphertextAuthentication)
		}, nil
	}
	s.load()

	if err := s.Delete("owned.ref"); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Delete error = %v, want directly detected ErrStoreCorrupt", err)
	}
	s.hooks.decryptSession = func(bool) (ciphertextDecryptor, error) {
		return func(ciphertextRecord, string) (string, error) { return "recovered", nil }, nil
	}
	assertCorruptionPreserved(t, s, evidence)
}

func TestTransientDecryptBackendFailureBlocksButDoesNotLatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	ciphertext := testCiphertext(3)
	evidence := writeSecretEvidence(t, path, map[string]string{"owned.ref": ciphertext})
	transient := errors.New("keyring temporarily unavailable")
	s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
	s.hooks.decryptSession = func(bool) (ciphertextDecryptor, error) {
		return func(ciphertextRecord, string) (string, error) { return "", transient }, nil
	}
	s.load()

	err := s.Health()
	if !errors.Is(err, transient) || errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Health error = %v, want retryable backend error only", err)
	}
	if err := s.Set("new.ref", "value"); !errors.Is(err, transient) || errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Set error = %v, want retryable backend error only", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, evidence) {
		t.Fatalf("transient failure changed evidence: %v", err)
	}

	s.hooks.decryptSession = testStoreHooks().decryptSession
	if err := s.Health(); err != nil {
		t.Fatalf("Health stayed latched after backend recovery: %v", err)
	}
}

func TestKeyChangeDuringSetCannotPublishMixedKeyCandidateOrLatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	legacy := testCiphertext(6)
	evidence := writeSecretEvidence(t, path, map[string]string{"existing.ref": legacy})
	stable := testStoreHooks()
	s := &Store{path: path, data: map[string]string{}, hooks: stable}
	sessions := 0
	s.hooks.decryptSession = func(hasCiphertext bool) (ciphertextDecryptor, error) {
		sessions++
		decrypt, err := stable.decryptSession(hasCiphertext)
		if err != nil {
			return nil, err
		}
		if sessions == 2 {
			return func(ciphertextRecord, string) (string, error) {
				return "", fmt.Errorf("%w: simulated key changed between validation and encryption", ErrCiphertextAuthentication)
			}, nil
		}
		return decrypt, nil
	}
	s.load()

	err := s.Set("new.ref", "new value")
	if !errors.Is(err, ErrCiphertextAuthentication) || errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Set error = %v, want non-latched candidate authentication error", err)
	}
	if _, exists := s.data["new.ref"]; exists {
		t.Fatal("mixed-key candidate entered memory")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(got, evidence) {
		t.Fatalf("mixed-key candidate changed durable evidence: %v", readErr)
	}
	s.hooks.decryptSession = stable.decryptSession
	if err := s.Health(); err != nil {
		t.Fatalf("candidate failure incorrectly latched store corruption: %v", err)
	}
}

func TestV2CiphertextSwapBetweenRefsIsDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	first := testBoundCiphertext("first.ref", "first value")
	second := testBoundCiphertext("second.ref", "second value")
	evidence := writeSecretEvidence(t, path, map[string]string{
		"first.ref":  second,
		"second.ref": first,
	})
	s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
	s.load()
	assertCorruptionPreserved(t, s, evidence)
}

func TestV3MasterKeyMismatchIsActionableNonLatchingAndRecoverable(t *testing.T) {
	correctKey := bytes.Repeat([]byte{0x41}, masterKeySize)
	wrongKey := bytes.Repeat([]byte{0x52}, masterKeySize)
	correctID := masterKeyID(correctKey)
	currentID := masterKeyID(wrongKey)
	encoded := encodeCiphertextRecord(bytes.Repeat([]byte{0x63}, minSecretCiphertextRawBytes+4), ciphertextV3, correctID)
	path := filepath.Join(t.TempDir(), "secrets.json")
	evidence := writeSecretEvidence(t, path, map[string]string{"owned.ref": encoded})
	newSession := func(bool) (ciphertextDecryptor, error) {
		return func(record ciphertextRecord, _ string) (string, error) {
			if err := validateRecordMasterKeyID(record, currentID); err != nil {
				return "", err
			}
			return "recovered plaintext", nil
		}, nil
	}
	s := &Store{path: path, data: map[string]string{}, hooks: storeHooks{decryptSession: newSession}}
	s.load()
	if err := s.Health(); !errors.Is(err, ErrMasterKeyMismatch) || errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("wrong-valid-key Health = %v, want non-latched ErrMasterKeyMismatch", err)
	}
	if err := s.Delete("owned.ref"); !errors.Is(err, ErrMasterKeyMismatch) || errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("wrong-valid-key Delete = %v, want non-latched ErrMasterKeyMismatch", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, evidence) {
		t.Fatalf("wrong-valid-key operations changed evidence: %v", err)
	}

	currentID = correctID
	value, found, err := s.GetChecked("owned.ref")
	if err != nil || !found || value != "recovered plaintext" {
		t.Fatalf("same-process restored-key read = (%q, %v, %v)", value, found, err)
	}
	restarted := &Store{path: path, data: map[string]string{}, hooks: storeHooks{decryptSession: newSession}}
	restarted.load()
	if err := restarted.Health(); err != nil {
		t.Fatalf("restart after key restore failed: %v", err)
	}
}

func TestLegacyCiphertextRemainsReadableAndOnlyExplicitWriteUpgrades(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	legacy := testCiphertext(4)
	untouched := testCiphertext(5)
	evidence := writeSecretEvidence(t, path, map[string]string{
		"legacy.ref":    legacy,
		"untouched.ref": untouched,
	})
	s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
	s.load()
	if value, found, err := s.GetChecked("legacy.ref"); err != nil || !found || value != "decrypted" {
		t.Fatalf("legacy GetChecked = (%q, %v, %v)", value, found, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, evidence) {
		t.Fatalf("read-only legacy access rewrote evidence: %v", err)
	}
	if err := s.Set("legacy.ref", "replacement"); err != nil {
		t.Fatal(err)
	}
	b, err := readSecretStoreFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := parseSecretStore(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loaded["legacy.ref"], ciphertextV2Prefix) {
		t.Fatalf("explicitly updated legacy ref was not upgraded: %q", loaded["legacy.ref"])
	}
	if loaded["untouched.ref"] != untouched {
		t.Fatal("unrelated legacy ciphertext was destructively rewritten")
	}
}

func TestDuplicateJSONRefsAreRejectedBeforeMapDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	first := testCiphertext(1)
	second := testCiphertext(2)
	// The escaped key decodes to the same ref as the first key.
	evidence := []byte(fmt.Sprintf(`{"owned.ref":%q,"owned\u002eref":%q}`, first, second))
	if err := os.WriteFile(path, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
	s.load()
	assertCorruptionPreserved(t, s, evidence)
	if len(s.data) != 0 {
		t.Fatalf("duplicate-key store partially decoded into memory: %#v", s.data)
	}
}

func TestSecretStoreAcquisitionAndSchemaLimits(t *testing.T) {
	tests := []struct {
		name     string
		evidence func() []byte
	}{
		{
			name: "invalid UTF-8 JSON",
			evidence: func() []byte {
				return []byte{'{', '"', 0xff, '"', ':', '"', 'A', '"', '}'}
			},
		},
		{
			name: "file bytes",
			evidence: func() []byte {
				return bytes.Repeat([]byte{' '}, maxSecretStoreBytes+1)
			},
		},
		{
			name: "ref bytes",
			evidence: func() []byte {
				return []byte(fmt.Sprintf(`{%q:%q}`, strings.Repeat("r", maxSecretRefBytes+1), testCiphertext(1)))
			},
		},
		{
			name: "ciphertext bytes",
			evidence: func() []byte {
				return []byte(fmt.Sprintf(`{"ref":%q}`, strings.Repeat("A", maxSecretCiphertextBytes+4)))
			},
		},
		{
			name: "entry count",
			evidence: func() []byte {
				entries := make(map[string]string, maxSecretEntries+1)
				for i := 0; i <= maxSecretEntries; i++ {
					entries[fmt.Sprintf("ref-%04d", i)] = testCiphertext(byte(i%250 + 1))
				}
				b, err := json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				return b
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secrets.json")
			evidence := tt.evidence()
			if err := os.WriteFile(path, evidence, 0o600); err != nil {
				t.Fatal(err)
			}
			s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
			s.load()
			assertCorruptionPreserved(t, s, evidence)
		})
	}
}

func TestSetRejectsInputLimitsBeforeEncryption(t *testing.T) {
	s := &Store{path: filepath.Join(t.TempDir(), "secrets.json"), data: map[string]string{}, hooks: testStoreHooks()}
	if err := s.Set(strings.Repeat("r", maxSecretRefBytes+1), "value"); err == nil {
		t.Fatal("oversized ref unexpectedly accepted")
	}
	if err := s.Set("ref", strings.Repeat("v", maxSecretPlaintextBytes+1)); err == nil {
		t.Fatal("oversized plaintext unexpectedly accepted")
	}
	if len(s.data) != 0 {
		t.Fatalf("rejected input mutated memory: %#v", s.data)
	}
}

func TestSetAndPersistenceRejectInvalidUTF8BeforeJSONEncoding(t *testing.T) {
	invalid := string([]byte{0xff, 0xfe})
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
	if err := s.Set(invalid, "value"); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid ref error = %v", err)
	}
	if err := s.Set("ref", invalid); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid value error = %v", err)
	}
	published, err := s.persistLocked(map[string]string{invalid: testCiphertext(1)})
	if err == nil || published {
		t.Fatalf("invalid candidate persist = (published=%v, err=%v)", published, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid UTF-8 candidate created a primary file: %v", err)
	}
}

func TestUnsafeManagedDirectoryAndNonRegularPrimaryAreRejected(t *testing.T) {
	t.Run("linked managed directory", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		pathInTarget := filepath.Join(target, "secrets.json")
		evidence := writeSecretEvidence(t, pathInTarget, map[string]string{"ref": testCiphertext(1)})
		linkedDir := filepath.Join(root, "managed")
		if err := os.Symlink(target, linkedDir); err != nil {
			t.Skipf("directory symlinks unavailable: %v", err)
		}
		s := &Store{path: filepath.Join(linkedDir, "secrets.json"), data: map[string]string{}, hooks: testStoreHooks()}
		s.load()
		if err := s.Health(); !errors.Is(err, ErrStoreUnsafePath) {
			t.Fatalf("Health error = %v, want ErrStoreUnsafePath", err)
		}
		if err := s.Set("ref", "replacement"); !errors.Is(err, ErrStoreUnsafePath) {
			t.Fatalf("Set error = %v, want ErrStoreUnsafePath", err)
		}
		got, err := os.ReadFile(pathInTarget)
		if err != nil || !bytes.Equal(got, evidence) {
			t.Fatalf("unsafe linked directory evidence changed: %v", err)
		}
	})

	t.Run("non-regular primary", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secrets.json")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
		s.load()
		if err := s.Health(); !errors.Is(err, ErrStoreUnsafePath) {
			t.Fatalf("Health error = %v, want ErrStoreUnsafePath", err)
		}
		if err := s.Delete("ref"); !errors.Is(err, ErrStoreUnsafePath) {
			t.Fatalf("Delete error = %v, want ErrStoreUnsafePath", err)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("non-regular evidence changed: %v", err)
		}
	})

	t.Run("linked primary", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		evidence := writeSecretEvidence(t, target, map[string]string{"ref": testCiphertext(1)})
		path := filepath.Join(dir, "secrets.json")
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("file symlinks unavailable: %v", err)
		}
		s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
		s.load()
		if err := s.Health(); !errors.Is(err, ErrStoreUnsafePath) {
			t.Fatalf("Health error = %v, want ErrStoreUnsafePath", err)
		}
		got, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(got, evidence) {
			t.Fatalf("linked-primary evidence changed: %v", err)
		}
	})
}

func TestPersistenceUsesUniquePrivateStagingAndReplacesExistingStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	fixedTemp := path + ".tmp"
	if err := os.WriteFile(fixedTemp, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{path: path, data: map[string]string{}, hooks: testStoreHooks()}
	var stagedPaths []string
	s.hooks.staged = func(stagedPath string) error {
		stagedPaths = append(stagedPaths, stagedPath)
		if stagedPath == fixedTemp {
			return errors.New("fixed temporary path was reused")
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(stagedPath)
			if err != nil {
				return err
			}
			if got := info.Mode().Perm(); got != 0o600 {
				return fmt.Errorf("staged mode = %o, want 600", got)
			}
		}
		return nil
	}
	if err := s.Set("ref", "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("ref", "second value"); err != nil {
		t.Fatal(err)
	}
	if len(stagedPaths) != 2 || stagedPaths[0] == stagedPaths[1] {
		t.Fatalf("staged paths = %v, want two unique paths", stagedPaths)
	}
	fixed, err := os.ReadFile(fixedTemp)
	if err != nil || string(fixed) != "do not touch" {
		t.Fatalf("legacy fixed temp was changed: %q, %v", fixed, err)
	}
	b, err := readSecretStoreFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := parseSecretStore(b)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["ref"] != testBoundCiphertext("ref", "second value") {
		t.Fatalf("published ciphertext = %q, want second candidate", loaded["ref"])
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".secrets-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("staged files were not cleaned up: %v", leftovers)
	}
}

func TestStagedVerificationFailurePreservesPreviousBytesAndMemory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	oldCiphertext := testCiphertext(1)
	evidence := writeSecretEvidence(t, path, map[string]string{"ref": oldCiphertext})
	s := &Store{path: path, data: map[string]string{"ref": oldCiphertext}, hooks: testStoreHooks()}
	s.hooks.staged = func(stagedPath string) error {
		return os.WriteFile(stagedPath, []byte(`{"ref":`), 0o600)
	}
	if err := s.Set("ref", "replacement"); err == nil || !strings.Contains(err.Error(), "verify staged") {
		t.Fatalf("Set error = %v, want staged verification failure", err)
	}
	if s.data["ref"] != oldCiphertext {
		t.Fatal("staged verification failure changed in-memory data")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, evidence) {
		t.Fatal("staged verification failure changed durable evidence")
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".secrets-*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("staged verification leftovers = %v, %v", leftovers, err)
	}
}

func TestRenameFailurePreservesPreviousBytesAndMemory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	oldCiphertext := testCiphertext(1)
	evidence := writeSecretEvidence(t, path, map[string]string{"ref": oldCiphertext})
	s := &Store{path: path, data: map[string]string{"ref": oldCiphertext}, hooks: testStoreHooks()}
	s.hooks.replace = func(string, string) error { return errors.New("injected rename failure") }
	if err := s.Set("ref", "replacement"); err == nil || !strings.Contains(err.Error(), "injected rename failure") {
		t.Fatalf("Set error = %v, want injected rename failure", err)
	}
	if s.data["ref"] != oldCiphertext {
		t.Fatal("rename failure changed in-memory data")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, evidence) {
		t.Fatal("rename failure changed durable evidence")
	}
}

func TestPostPublishSyncFailureKeepsMemoryConsistentWithVisibleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	oldCiphertext := testCiphertext(1)
	writeSecretEvidence(t, path, map[string]string{"ref": oldCiphertext})
	s := &Store{path: path, data: map[string]string{"ref": oldCiphertext}, hooks: testStoreHooks()}
	s.hooks.syncDir = func(string) error { return errors.New("injected directory sync failure") }
	err := s.Set("ref", "replacement")
	if err == nil || !strings.Contains(err.Error(), "injected directory sync failure") {
		t.Fatalf("Set error = %v, want directory sync failure", err)
	}
	want := testBoundCiphertext("ref", "replacement")
	if s.data["ref"] != want {
		t.Fatalf("memory retained %q after publication, want visible candidate %q", s.data["ref"], want)
	}
	b, err := readSecretStoreFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := parseSecretStore(b)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["ref"] != want {
		t.Fatalf("visible file has %q, want %q", loaded["ref"], want)
	}
}

func TestReplaceIsCountNeutralAtFullCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	data := make(map[string]string, maxSecretEntries)
	data["legacy.ref"] = testCiphertext(1)
	for i := 1; i < maxSecretEntries; i++ {
		data[fmt.Sprintf("other.%d", i)] = testCiphertext(byte(i%250 + 2))
	}
	writeSecretEvidence(t, path, data)
	s := &Store{path: path, data: cloneData(data), hooks: testStoreHooks()}

	if err := s.Replace("legacy.ref", "owned.ref", "explicit replacement"); err != nil {
		t.Fatal(err)
	}
	if len(s.data) != maxSecretEntries {
		t.Fatalf("entry count = %d, want %d", len(s.data), maxSecretEntries)
	}
	if _, found := s.data["legacy.ref"]; found {
		t.Fatal("legacy ref survived replacement")
	}
	got, found, err := s.GetChecked("owned.ref")
	if err != nil || !found || got != "explicit replacement" {
		t.Fatalf("owned GetChecked = (%q, %v, %v)", got, found, err)
	}
}

func TestReplaceOverwritesDestinationInOneCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	data := map[string]string{
		"legacy.ref": testCiphertext(1),
		"owned.ref":  testBoundCiphertext("owned.ref", "stale destination"),
	}
	writeSecretEvidence(t, path, data)
	s := &Store{path: path, data: cloneData(data), hooks: testStoreHooks()}

	if err := s.Replace("legacy.ref", "owned.ref", "explicit replacement"); err != nil {
		t.Fatal(err)
	}
	if len(s.data) != 1 {
		t.Fatalf("entry count = %d, want 1", len(s.data))
	}
	got, found, err := s.GetChecked("owned.ref")
	if err != nil || !found || got != "explicit replacement" {
		t.Fatalf("owned GetChecked = (%q, %v, %v)", got, found, err)
	}
}

func TestReplaceMissingOldRefPreservesEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	data := map[string]string{"unrelated.ref": testCiphertext(1)}
	evidence := writeSecretEvidence(t, path, data)
	s := &Store{path: path, data: cloneData(data), hooks: testStoreHooks()}

	if err := s.Replace("missing.ref", "owned.ref", "replacement"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Replace error = %v, want missing-old error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, evidence) || !maps.Equal(s.data, data) {
		t.Fatal("missing-old replacement changed evidence or memory")
	}
}

func TestReplaceRenameFailurePreservesOldRefAndEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	data := map[string]string{"legacy.ref": testCiphertext(1)}
	evidence := writeSecretEvidence(t, path, data)
	s := &Store{path: path, data: cloneData(data), hooks: testStoreHooks()}
	s.hooks.replace = func(string, string) error { return errors.New("injected replacement publish failure") }

	if err := s.Replace("legacy.ref", "owned.ref", "replacement"); err == nil || !strings.Contains(err.Error(), "injected replacement publish failure") {
		t.Fatalf("Replace error = %v, want injected publish failure", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, evidence) || !maps.Equal(s.data, data) {
		t.Fatal("pre-publication failure changed old evidence or memory")
	}
}

func TestReplaceRejectsUnauthenticatedCandidateWithoutPublishing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	data := map[string]string{"legacy.ref": testCiphertext(1)}
	evidence := writeSecretEvidence(t, path, data)
	s := &Store{path: path, data: cloneData(data), hooks: testStoreHooks()}
	s.hooks.encrypt = func(value, _ string, _ bool) (string, error) {
		return testBoundCiphertext("wrong.ref", value), nil
	}

	err := s.Replace("legacy.ref", "owned.ref", "replacement")
	if err == nil || !errors.Is(err, ErrCiphertextAuthentication) {
		t.Fatalf("Replace error = %v, want authentication failure", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, evidence) || !maps.Equal(s.data, data) {
		t.Fatal("candidate authentication failure changed old evidence or memory")
	}
}

func TestReplacePostPublishSyncFailureKeepsVisibleForwardCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	data := map[string]string{"legacy.ref": testCiphertext(1)}
	writeSecretEvidence(t, path, data)
	s := &Store{path: path, data: cloneData(data), hooks: testStoreHooks()}
	s.hooks.syncDir = func(string) error { return errors.New("injected replacement directory sync failure") }

	err := s.Replace("legacy.ref", "owned.ref", "replacement")
	if err == nil || !strings.Contains(err.Error(), "injected replacement directory sync failure") {
		t.Fatalf("Replace error = %v, want directory sync failure", err)
	}
	if _, found := s.data["legacy.ref"]; found {
		t.Fatal("memory retained old ref after published replacement")
	}
	if got := s.data["owned.ref"]; got != testBoundCiphertext("owned.ref", "replacement") {
		t.Fatalf("memory scoped ciphertext = %q", got)
	}
	b, readErr := readSecretStoreFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	loaded, parseErr := parseSecretStore(b)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if _, found := loaded["legacy.ref"]; found || loaded["owned.ref"] != s.data["owned.ref"] {
		t.Fatalf("visible replacement candidate = %#v, memory = %#v", loaded, s.data)
	}
}
