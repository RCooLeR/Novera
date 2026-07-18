package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
)

// These helpers are platform-neutral so the non-Windows envelope and recovery
// policy can be executed in tests on every development OS. Production Windows
// writes continue to use DPAPI; production Linux/macOS call these with the
// durable keyring key.
func aeadFromKey(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptStringWithKey(key []byte, plain, ref string, format ciphertextFormat, random io.Reader) (string, error) {
	gcm, err := aeadFromKey(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", err
	}
	keyID := ""
	var aad []byte
	if format == ciphertextV2 {
		aad = secretCiphertextAAD(ciphertextV2, "", ref)
	} else if format == ciphertextV3 {
		keyID = masterKeyID(key)
		aad = secretCiphertextAAD(ciphertextV3, keyID, ref)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), aad)
	return encodeCiphertextRecord(ct, format, keyID), nil
}

func newCiphertextDecryptorWithKey(key []byte) (ciphertextDecryptor, error) {
	gcm, err := aeadFromKey(key)
	if err != nil {
		return nil, err
	}
	keyID := masterKeyID(key)
	return func(record ciphertextRecord, ref string) (string, error) {
		ns := gcm.NonceSize()
		if len(record.raw) < ns {
			return "", fmt.Errorf("%w: ciphertext nonce is truncated", ErrCiphertextAuthentication)
		}
		var aad []byte
		switch record.format {
		case ciphertextV2:
			aad = secretCiphertextAAD(ciphertextV2, "", ref)
		case ciphertextV3:
			if err := validateRecordMasterKeyID(record, keyID); err != nil {
				return "", err
			}
			aad = secretCiphertextAAD(ciphertextV3, record.keyID, ref)
		}
		plain, err := gcm.Open(nil, record.raw[:ns], record.raw[ns:], aad)
		if err != nil {
			if record.format != ciphertextV3 {
				return "", &CiphertextRecoveryAmbiguousError{Service: keyringService, Account: keyringKeyName}
			}
			return "", fmt.Errorf("%w: AES-GCM rejected the ciphertext", ErrCiphertextAuthentication)
		}
		return string(plain), nil
	}, nil
}
