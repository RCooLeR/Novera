package secret

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	masterKeySize           = 32
	masterKeyRecordPrefix   = "novera-master-key:"
	masterKeyV1Prefix       = masterKeyRecordPrefix + "v1:"
	masterKeyBase64Bytes    = 44 // canonical padded base64 for exactly 32 bytes
	maxMasterKeyRecordBytes = len(masterKeyV1Prefix) + masterKeyBase64Bytes
	masterKeyIDHexBytes     = sha256.Size * 2
	keyringService          = "Novera"
	keyringKeyName          = "secret-store-master-key"
)

var (
	// ErrCorruptMasterKey identifies keyring material that exists but cannot be
	// interpreted safely. The existing keyring value is never included in the
	// diagnostic and must be left untouched for deliberate recovery.
	ErrCorruptMasterKey = errors.New("secret-store master key is corrupt")
	// ErrMasterKeyUnavailable identifies an absent key when encrypted values
	// already exist. Generating a replacement in that state would make the
	// ciphertext permanently unrecoverable, so callers must fail closed.
	ErrMasterKeyUnavailable = errors.New("secret-store master key is unavailable")
	// ErrMasterKeyMismatch identifies valid keyring material whose non-secret
	// fingerprint does not match a versioned ciphertext envelope.
	ErrMasterKeyMismatch = errors.New("secret-store master key does not match ciphertext")
	// ErrCiphertextRecoveryAmbiguous identifies legacy/v2 authentication
	// failures that cannot safely distinguish a wrong key from damaged data.
	ErrCiphertextRecoveryAmbiguous = errors.New("secret-store key or legacy ciphertext requires recovery")
)

// MasterKeyMaterialError gives an actionable keyring coordinate without
// leaking the raw master-key material into logs or renderer-visible errors.
type MasterKeyMaterialError struct {
	Service string
	Account string
	Reason  string
}

func (e *MasterKeyMaterialError) Error() string {
	return fmt.Sprintf(
		"OS keyring service %q account %q contains invalid Novera master-key material (%s); it was left unchanged; restore the original key or deliberately reset the encrypted secret store and key together",
		e.Service,
		e.Account,
		e.Reason,
	)
}

func (e *MasterKeyMaterialError) Is(target error) bool { return target == ErrCorruptMasterKey }

// MasterKeyUnavailableError reports the exact keyring coordinate whose value
// must be restored. It intentionally does not offer automatic regeneration.
type MasterKeyUnavailableError struct {
	Service string
	Account string
}

func (e *MasterKeyUnavailableError) Error() string {
	return fmt.Sprintf(
		"OS keyring service %q account %q has no Novera master key while encrypted secrets still exist; no replacement was created; restore the original key or deliberately reset the encrypted secret store and key together",
		e.Service,
		e.Account,
	)
}

func (e *MasterKeyUnavailableError) Is(target error) bool { return target == ErrMasterKeyUnavailable }

// MasterKeyMismatchError reports a validly encoded but incorrect durable key.
// It intentionally omits both key material and fingerprints from diagnostics.
type MasterKeyMismatchError struct {
	Service string
	Account string
}

func (e *MasterKeyMismatchError) Error() string {
	return fmt.Sprintf(
		"OS keyring service %q account %q contains a valid Novera master key, but it does not match the encrypted secret store; the store was left unchanged and mutations are blocked; restore the original key and retry or restart",
		e.Service,
		e.Account,
	)
}

func (e *MasterKeyMismatchError) Is(target error) bool { return target == ErrMasterKeyMismatch }

// CiphertextRecoveryAmbiguousError is used only for records created before key
// fingerprints existed. Those records cannot prove whether the key or data is
// wrong, so the error preserves both and avoids a permanent corruption latch.
type CiphertextRecoveryAmbiguousError struct {
	Service string
	Account string
}

func (e *CiphertextRecoveryAmbiguousError) Error() string {
	return fmt.Sprintf(
		"legacy encrypted secret data could not be authenticated with the valid key in OS keyring service %q account %q; the store was left unchanged and mutations are blocked; restore the original key or an intact store together, then retry or restart",
		e.Service,
		e.Account,
	)
}

func (e *CiphertextRecoveryAmbiguousError) Is(target error) bool {
	return target == ErrCiphertextRecoveryAmbiguous
}

func corruptMasterKey(reason string) error {
	return &MasterKeyMaterialError{Service: keyringService, Account: keyringKeyName, Reason: reason}
}

func masterKeyID(key []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte("Novera master-key fingerprint v1\x00"))
	_, _ = h.Write(key)
	return hex.EncodeToString(h.Sum(nil))
}

func validateRecordMasterKeyID(record ciphertextRecord, actualKeyID string) error {
	if record.format != ciphertextV3 {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(record.keyID), []byte(actualKeyID)) != 1 {
		return &MasterKeyMismatchError{Service: keyringService, Account: keyringKeyName}
	}
	return nil
}

// decodeMasterKeyMaterial accepts the unversioned 32-byte base64 value written
// by earlier Novera releases and the versioned v1 record written by current
// releases. A future/unknown version is never guessed or rewritten.
func decodeMasterKeyMaterial(encoded string) ([]byte, error) {
	// Bound hostile/corrupt keyring responses before trimming, prefix scans, or
	// base64 allocation. Valid legacy and v1 records have fixed encoded sizes.
	if len(encoded) > maxMasterKeyRecordBytes {
		return nil, corruptMasterKey("record exceeds the maximum encoded length")
	}
	if strings.TrimSpace(encoded) != encoded || strings.ContainsAny(encoded, "\r\n\t") {
		return nil, corruptMasterKey("unexpected whitespace")
	}
	payload := encoded
	if strings.HasPrefix(encoded, masterKeyRecordPrefix) {
		if !strings.HasPrefix(encoded, masterKeyV1Prefix) {
			return nil, corruptMasterKey("unsupported key record version")
		}
		payload = strings.TrimPrefix(encoded, masterKeyV1Prefix)
	}
	if len(payload) != masterKeyBase64Bytes {
		return nil, corruptMasterKey(fmt.Sprintf("encoded payload length is %d bytes, expected %d", len(payload), masterKeyBase64Bytes))
	}
	key, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil {
		return nil, corruptMasterKey("invalid base64 encoding")
	}
	if len(key) != masterKeySize {
		return nil, corruptMasterKey(fmt.Sprintf("decoded length is %d bytes, expected %d", len(key), masterKeySize))
	}
	return key, nil
}

// persistAndVerifyMasterKeyRecord prevents first-run encryption under an
// ephemeral or inconsistently persisted key. The keyring record is read back,
// decoded under the same strict policy, and compared without exposing it.
func persistAndVerifyMasterKeyRecord(record string, persist func(string) error, readBack func() (string, error)) error {
	if persist == nil || readBack == nil {
		return errors.New("master-key persistence and read-back functions are required")
	}
	writtenKey, err := decodeMasterKeyMaterial(record)
	if err != nil {
		return fmt.Errorf("verify generated master-key record: %w", err)
	}
	if err := persist(record); err != nil {
		return err
	}
	storedRecord, err := readBack()
	if err != nil {
		return fmt.Errorf("read back newly written master-key record: %w", err)
	}
	storedKey, err := decodeMasterKeyMaterial(storedRecord)
	if err != nil {
		return fmt.Errorf("validate newly written master-key record: %w", err)
	}
	if subtle.ConstantTimeCompare(writtenKey, storedKey) != 1 {
		return errors.New("newly written master-key record does not match its read-back value")
	}
	return nil
}

func encodeMasterKeyMaterial(key []byte) (string, error) {
	if len(key) != masterKeySize {
		return "", fmt.Errorf("encode master key: expected %d bytes, got %d", masterKeySize, len(key))
	}
	return masterKeyV1Prefix + base64.StdEncoding.EncodeToString(key), nil
}

// resolveMasterKeyMaterial is the policy boundary between lookup and creation.
// It never calls persist for malformed existing material, and only creates a
// key when the caller has proved there is no ciphertext depending on an older
// key. This helper is platform-neutral so the policy is regression-tested on
// every supported development OS.
func resolveMasterKeyMaterial(encoded string, found, allowCreate bool, persist func(string) error, random io.Reader) ([]byte, error) {
	if found {
		return decodeMasterKeyMaterial(encoded)
	}
	if !allowCreate {
		return nil, &MasterKeyUnavailableError{Service: keyringService, Account: keyringKeyName}
	}
	key := make([]byte, masterKeySize)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, fmt.Errorf("generate secret-store master key: %w", err)
	}
	record, err := encodeMasterKeyMaterial(key)
	if err != nil {
		return nil, err
	}
	if err := persist(record); err != nil {
		return nil, fmt.Errorf("write secret-store master key to OS keyring service %q account %q: %w", keyringService, keyringKeyName, err)
	}
	return key, nil
}
