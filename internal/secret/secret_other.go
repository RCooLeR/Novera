//go:build !windows

package secret

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// On non-Windows platforms secrets are AES-256-GCM encrypted with a per-user
// master key kept in the OS keyring (macOS Keychain / Linux Secret Service via
// libsecret). The master key, not the secrets, lives in the keyring; encrypted
// values live in the 0600 secrets file. We fail closed if the keyring is
// unavailable and never replace existing malformed key material.
var masterKeyMu sync.Mutex

// masterKey fetches the durable key on every operation. Caching it would let a
// running process keep writing ciphertext after the durable keyring entry was
// removed or corrupted. Creation is allowed only for a proven-empty store.
func masterKey(allowCreate bool) ([]byte, error) {
	masterKeyMu.Lock()
	defer masterKeyMu.Unlock()
	enc, err := keyring.Get(keyringService, keyringKeyName)
	if err == nil {
		return resolveMasterKeyMaterial(enc, true, false, nil, nil)
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("read secret-store master key from OS keyring service %q account %q: %w", keyringService, keyringKeyName, err)
	}
	return resolveMasterKeyMaterial("", false, allowCreate, func(record string) error {
		return persistAndVerifyMasterKeyRecord(record, func(value string) error {
			return keyring.Set(keyringService, keyringKeyName, value)
		}, func() (string, error) {
			return keyring.Get(keyringService, keyringKeyName)
		})
	}, rand.Reader)
}

// encryptionBackendHealth validates the durable key without ever creating or
// rewriting it. A missing key is healthy only when there is no ciphertext that
// could depend on an older key.
func encryptionBackendHealth(hasEncryptedSecrets bool) error {
	masterKeyMu.Lock()
	defer masterKeyMu.Unlock()
	enc, err := keyring.Get(keyringService, keyringKeyName)
	if err == nil {
		_, err = decodeMasterKeyMaterial(enc)
		return err
	}
	if errors.Is(err, keyring.ErrNotFound) {
		if hasEncryptedSecrets {
			return &MasterKeyUnavailableError{Service: keyringService, Account: keyringKeyName}
		}
		return nil
	}
	return fmt.Errorf("read secret-store master key from OS keyring service %q account %q: %w", keyringService, keyringKeyName, err)
}

func encryptString(plain, ref string, allowKeyCreation bool) (string, error) {
	if plain == "" {
		return "", nil
	}
	key, err := masterKey(allowKeyCreation)
	if err != nil {
		return "", err
	}
	return encryptStringWithKey(key, plain, ref, ciphertextV3, rand.Reader)
}

// newCiphertextDecryptor takes exactly one immutable master-key snapshot for a
// validation pass. A keyring outage/change therefore cannot occur between the
// health check and individual ciphertext authentication and be mislabeled as
// permanent store corruption.
func newCiphertextDecryptor(hasEncryptedSecrets bool) (ciphertextDecryptor, error) {
	if !hasEncryptedSecrets {
		if err := encryptionBackendHealth(false); err != nil {
			return nil, err
		}
		return func(ciphertextRecord, string) (string, error) { return "", nil }, nil
	}
	key, err := masterKey(false)
	if err != nil {
		return nil, err
	}
	return newCiphertextDecryptorWithKey(key)
}
