//go:build !windows

package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"sync"

	"github.com/zalando/go-keyring"
)

// On non-Windows platforms secrets are AES-256-GCM encrypted with a per-user
// master key kept in the OS keyring (macOS Keychain / Linux Secret Service via
// libsecret). The master key — not the secrets — lives in the keyring; the
// encrypted values live in the 0600 secrets file. This is real at-rest
// encryption: a leak of the config dir no longer leaks credentials. We fail
// closed if the keyring is unavailable rather than silently storing plaintext.
const (
	keyringService = "Novera"
	keyringKeyName = "secret-store-master-key"
)

var (
	masterKeyMu sync.Mutex
	cachedKey   []byte
)

// masterKey returns the per-user AES-256 key, fetching it from the OS keyring or
// generating + persisting one there on first use.
func masterKey() ([]byte, error) {
	masterKeyMu.Lock()
	defer masterKeyMu.Unlock()
	if cachedKey != nil {
		return cachedKey, nil
	}
	enc, err := keyring.Get(keyringService, keyringKeyName)
	if err == nil {
		if key, derr := base64.StdEncoding.DecodeString(enc); derr == nil && len(key) == 32 {
			cachedKey = key
			return key, nil
		}
		// Stored key is corrupt/wrong size — regenerate (existing secrets become
		// undecryptable, which Get() surfaces rather than masking).
	} else if !errors.Is(err, keyring.ErrNotFound) {
		// Keyring backend unavailable (e.g. headless, no Secret Service) — fail
		// closed instead of falling back to plaintext.
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := keyring.Set(keyringService, keyringKeyName, base64.StdEncoding.EncodeToString(key)); err != nil {
		return nil, err
	}
	cachedKey = key
	return key, nil
}

func aead() (cipher.AEAD, error) {
	key, err := masterKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptString(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	gcm, err := aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	// Prepend the nonce so decrypt can recover it; result is base64 for JSON.
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func decryptString(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	gcm, err := aead()
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
