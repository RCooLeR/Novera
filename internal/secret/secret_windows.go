//go:build windows

package secret

import "github.com/billgraziano/dpapi"

// encryptString protects the value with Windows DPAPI (per-user). Output is
// base64 ciphertext that only this Windows user account can decrypt.
func encryptString(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	return dpapi.Encrypt(plain)
}

func decryptString(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	return dpapi.Decrypt(enc)
}
