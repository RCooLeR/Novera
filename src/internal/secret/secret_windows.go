//go:build windows

package secret

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/billgraziano/dpapi"
	"golang.org/x/sys/windows"
)

// encryptString protects the value with Windows DPAPI (per-user). Output is
// base64 ciphertext that only this Windows user account can decrypt.
func encryptString(plain, ref string, _ bool) (string, error) {
	if plain == "" {
		return "", nil
	}
	encoded, err := dpapi.EncryptEntropy(plain, string(secretCiphertextAAD(ciphertextV2, "", ref)))
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("DPAPI returned invalid ciphertext encoding: %w", err)
	}
	return encodeCiphertextRecord(raw, ciphertextV2), nil
}

// Windows DPAPI does not use a separately managed application master key.
func encryptionBackendHealth(bool) error { return nil }

func newCiphertextDecryptor(bool) (ciphertextDecryptor, error) {
	return func(record ciphertextRecord, ref string) (string, error) {
		if record.format == ciphertextV3 {
			return "", errors.New("v3 ciphertext uses a non-Windows master-key envelope and cannot be opened with DPAPI")
		}
		encoded := base64.StdEncoding.EncodeToString(record.raw)
		var (
			plaintext string
			err       error
		)
		if record.format == ciphertextV2 {
			plaintext, err = dpapi.DecryptEntropy(encoded, string(secretCiphertextAAD(ciphertextV2, "", ref)))
		} else {
			plaintext, err = dpapi.Decrypt(encoded)
		}
		if err == nil {
			return plaintext, nil
		}
		// ERROR_INVALID_DATA is DPAPI's definitive response for a ciphertext
		// that cannot authenticate under this user/entropy. Other OS failures
		// are returned as retryable backend errors and are not lifetime-latched.
		if errors.Is(err, windows.ERROR_INVALID_DATA) {
			return "", fmt.Errorf("%w: DPAPI rejected the ciphertext", ErrCiphertextAuthentication)
		}
		return "", fmt.Errorf("DPAPI decrypt backend failed: %w", err)
	}, nil
}
