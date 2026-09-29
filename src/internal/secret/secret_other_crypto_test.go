package secret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestV3WrongValidMasterKeyIsDetectedBeforeOpenAndCanRecover(t *testing.T) {
	correctKey := bytes.Repeat([]byte{0x31}, masterKeySize)
	wrongKey := bytes.Repeat([]byte{0x42}, masterKeySize)
	encoded, err := encryptStringWithKey(
		correctKey,
		"recoverable secret",
		"owned.ref",
		ciphertextV3,
		bytes.NewReader(bytes.Repeat([]byte{0x77}, 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := decodeCiphertextRecord(encoded)
	if err != nil {
		t.Fatal(err)
	}
	wrongDecryptor, err := newCiphertextDecryptorWithKey(wrongKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongDecryptor(record, "owned.ref"); !errors.Is(err, ErrMasterKeyMismatch) || errors.Is(err, ErrCiphertextAuthentication) {
		t.Fatalf("wrong-key error = %v, want only ErrMasterKeyMismatch", err)
	}

	path := filepath.Join(t.TempDir(), "secrets.json")
	evidence := writeSecretEvidence(t, path, map[string]string{"owned.ref": encoded})
	currentKey := wrongKey
	newSession := func(bool) (ciphertextDecryptor, error) {
		return newCiphertextDecryptorWithKey(currentKey)
	}
	s := &Store{path: path, data: map[string]string{}, hooks: storeHooks{decryptSession: newSession}}
	s.load()
	if err := s.Health(); !errors.Is(err, ErrMasterKeyMismatch) || errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("wrong-key Health = %v, want non-latched ErrMasterKeyMismatch", err)
	}
	if err := s.Delete("owned.ref"); !errors.Is(err, ErrMasterKeyMismatch) || errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("wrong-key Delete = %v, want non-latched ErrMasterKeyMismatch", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, evidence) {
		t.Fatalf("wrong-key attempt changed evidence: %v", err)
	}

	// Restoring the original key recovers the same Store without a permanent
	// corruption latch.
	currentKey = correctKey
	value, found, err := s.GetChecked("owned.ref")
	if err != nil || !found || value != "recoverable secret" {
		t.Fatalf("same-process recovery = (%q, %v, %v)", value, found, err)
	}
	// A restart over the same untouched evidence also recovers cleanly.
	restarted := &Store{path: path, data: map[string]string{}, hooks: storeHooks{decryptSession: newSession}}
	restarted.load()
	if err := restarted.Health(); err != nil {
		t.Fatalf("restart after key restore failed: %v", err)
	}
}

func TestV3MatchingKeyMakesAuthenticationFailureDefinitive(t *testing.T) {
	key := bytes.Repeat([]byte{0x53}, masterKeySize)
	encoded, err := encryptStringWithKey(
		key,
		"secret",
		"owned.ref",
		ciphertextV3,
		bytes.NewReader(bytes.Repeat([]byte{0x61}, 32)),
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := decodeCiphertextRecord(encoded)
	if err != nil {
		t.Fatal(err)
	}
	record.raw[len(record.raw)-1] ^= 0xff
	decrypt, err := newCiphertextDecryptorWithKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt(record, "owned.ref"); !errors.Is(err, ErrCiphertextAuthentication) || errors.Is(err, ErrMasterKeyMismatch) {
		t.Fatalf("damaged v3 error = %v, want definitive authentication failure", err)
	}
}

func TestLegacyAndV2WrongKeyFailuresRemainRecoverableAndReadable(t *testing.T) {
	correctKey := bytes.Repeat([]byte{0x64}, masterKeySize)
	wrongKey := bytes.Repeat([]byte{0x75}, masterKeySize)
	for _, format := range []ciphertextFormat{ciphertextLegacy, ciphertextV2} {
		encoded, err := encryptStringWithKey(
			correctKey,
			"compatible secret",
			"legacy.ref",
			format,
			bytes.NewReader(bytes.Repeat([]byte{byte(format)}, 32)),
		)
		if err != nil {
			t.Fatal(err)
		}
		record, err := decodeCiphertextRecord(encoded)
		if err != nil {
			t.Fatal(err)
		}
		wrongDecryptor, err := newCiphertextDecryptorWithKey(wrongKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wrongDecryptor(record, "legacy.ref"); !errors.Is(err, ErrCiphertextRecoveryAmbiguous) || errors.Is(err, ErrCiphertextAuthentication) {
			t.Fatalf("format %v wrong-key error = %v, want recoverable ambiguity", format, err)
		}
		correctDecryptor, err := newCiphertextDecryptorWithKey(correctKey)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := correctDecryptor(record, "legacy.ref")
		if err != nil || plain != "compatible secret" {
			t.Fatalf("format %v compatibility read = (%q, %v)", format, plain, err)
		}
	}
}
