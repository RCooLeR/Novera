package secret

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestExistingMalformedMasterKeyNeverWritesReplacement(t *testing.T) {
	cases := map[string]string{
		"malformed base64": "%%%not-base64%%%",
		"truncated key":    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, masterKeySize-1)),
		"wrong version":    masterKeyRecordPrefix + "v99:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, masterKeySize)),
		"embedded newline": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, masterKeySize)) + "\n",
		"oversized record": strings.Repeat("A", maxMasterKeyRecordBytes+1),
	}
	for name, evidence := range cases {
		t.Run(name, func(t *testing.T) {
			original := evidence
			writes := 0
			_, err := resolveMasterKeyMaterial(evidence, true, true, func(string) error {
				writes++
				return nil
			}, bytes.NewReader(bytes.Repeat([]byte{0x44}, masterKeySize)))
			if !errors.Is(err, ErrCorruptMasterKey) {
				t.Fatalf("error = %v, want ErrCorruptMasterKey", err)
			}
			if writes != 0 {
				t.Fatalf("persist called %d times for corrupt existing material", writes)
			}
			if evidence != original {
				t.Fatal("test evidence was modified")
			}
			if !strings.Contains(err.Error(), keyringService) || !strings.Contains(err.Error(), keyringKeyName) {
				t.Fatalf("diagnostic lacks keyring coordinate: %v", err)
			}
			if strings.Contains(err.Error(), original) {
				t.Fatalf("diagnostic leaked raw keyring material: %v", err)
			}
		})
	}
}

func TestMasterKeyReadBackMustMatchBeforeUse(t *testing.T) {
	key := bytes.Repeat([]byte{0x6c}, masterKeySize)
	record, err := encodeMasterKeyMaterial(key)
	if err != nil {
		t.Fatal(err)
	}
	var persisted string
	if err := persistAndVerifyMasterKeyRecord(record, func(value string) error {
		persisted = value
		return nil
	}, func() (string, error) {
		return persisted, nil
	}); err != nil {
		t.Fatalf("matching read-back failed: %v", err)
	}

	other, err := encodeMasterKeyMaterial(bytes.Repeat([]byte{0x7d}, masterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistAndVerifyMasterKeyRecord(record, func(string) error { return nil }, func() (string, error) {
		return other, nil
	}); err == nil || strings.Contains(err.Error(), record) || strings.Contains(err.Error(), other) {
		t.Fatalf("mismatched read-back error = %v", err)
	}
}

func TestInvalidGeneratedMasterKeyRecordIsRejectedBeforePersistence(t *testing.T) {
	writes := 0
	err := persistAndVerifyMasterKeyRecord(strings.Repeat("A", maxMasterKeyRecordBytes+1), func(string) error {
		writes++
		return nil
	}, func() (string, error) {
		t.Fatal("read-back must not run")
		return "", nil
	})
	if !errors.Is(err, ErrCorruptMasterKey) {
		t.Fatalf("error = %v, want ErrCorruptMasterKey", err)
	}
	if writes != 0 {
		t.Fatalf("invalid record was persisted %d times", writes)
	}
}

func TestMasterKeyMaterialSupportsLegacyAndVersionedV1(t *testing.T) {
	want := bytes.Repeat([]byte{0x5a}, masterKeySize)
	legacy := base64.StdEncoding.EncodeToString(want)
	versioned, err := encodeMasterKeyMaterial(want)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"legacy": legacy, "versioned": versioned} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeMasterKeyMaterial(value)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("decoded key differs: %x", got)
			}
		})
	}
}

func TestMasterKeyIDIsStableNonSecretFingerprint(t *testing.T) {
	first := bytes.Repeat([]byte{0x11}, masterKeySize)
	second := bytes.Repeat([]byte{0x22}, masterKeySize)
	firstID := masterKeyID(first)
	if len(firstID) != masterKeyIDHexBytes {
		t.Fatalf("key ID length = %d, want %d", len(firstID), masterKeyIDHexBytes)
	}
	if firstID != masterKeyID(first) {
		t.Fatal("key ID is not deterministic")
	}
	if firstID == masterKeyID(second) {
		t.Fatal("distinct test keys produced the same key ID")
	}
	if strings.Contains(firstID, base64.StdEncoding.EncodeToString(first)) {
		t.Fatal("key ID contains raw encoded key material")
	}
}

func TestMissingMasterKeyWithCiphertextFailsWithoutCreation(t *testing.T) {
	writes := 0
	_, err := resolveMasterKeyMaterial("", false, false, func(string) error {
		writes++
		return nil
	}, bytes.NewReader(bytes.Repeat([]byte{0x41}, masterKeySize)))
	if !errors.Is(err, ErrMasterKeyUnavailable) {
		t.Fatalf("error = %v, want ErrMasterKeyUnavailable", err)
	}
	if writes != 0 {
		t.Fatalf("persist called %d times", writes)
	}
}

func TestFreshMasterKeyCreationIsVersionedAndPersistenceBound(t *testing.T) {
	want := bytes.Repeat([]byte{0x7b}, masterKeySize)
	var persisted string
	got, err := resolveMasterKeyMaterial("", false, true, func(record string) error {
		persisted = record
		return nil
	}, bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generated key differs: %x", got)
	}
	if !strings.HasPrefix(persisted, masterKeyV1Prefix) {
		t.Fatalf("persisted record %q is not versioned", persisted)
	}
	decoded, err := decodeMasterKeyMaterial(persisted)
	if err != nil || !bytes.Equal(decoded, want) {
		t.Fatalf("persisted record does not round-trip: key=%x err=%v", decoded, err)
	}
}

func TestMasterKeyCreationFaultDoesNotReturnEphemeralKey(t *testing.T) {
	wantErr := errors.New("keyring write failed")
	got, err := resolveMasterKeyMaterial("", false, true, func(string) error {
		return wantErr
	}, bytes.NewReader(bytes.Repeat([]byte{0x22}, masterKeySize)))
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped persistence error", err)
	}
	if got != nil {
		t.Fatalf("returned an unpersisted key: %x", got)
	}
}

func TestMasterKeyCreationRejectsShortRandomRead(t *testing.T) {
	_, err := resolveMasterKeyMaterial("", false, true, func(string) error {
		t.Fatal("persist must not run after random-source failure")
		return nil
	}, io.LimitReader(bytes.NewReader([]byte("short")), 5))
	if err == nil {
		t.Fatal("short random source unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "generate secret-store master key") {
		t.Fatalf("unexpected diagnostic: %v", err)
	}
}

func TestEncodeMasterKeyRejectsWrongSize(t *testing.T) {
	if _, err := encodeMasterKeyMaterial(make([]byte, masterKeySize+1)); err == nil {
		t.Fatal("wrong-size key unexpectedly encoded")
	}
	if got := fmt.Sprint(ErrCorruptMasterKey); got == "" {
		t.Fatal("sentinel unexpectedly empty")
	}
}
