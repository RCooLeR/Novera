//go:build windows

package secret

import (
	"errors"
	"testing"
)

func TestDPAPIV2RoundTripBindsCiphertextToRef(t *testing.T) {
	encoded, err := encryptString("platform secret", "owned.ref", false)
	if err != nil {
		t.Skipf("DPAPI encryption unavailable for current test identity: %v", err)
	}
	record, err := decodeCiphertextRecord(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if record.format != ciphertextV2 {
		t.Fatalf("format = %v, want v2", record.format)
	}
	decrypt, err := newCiphertextDecryptor(true)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decrypt(record, "owned.ref")
	if err != nil || plain != "platform secret" {
		t.Fatalf("round trip = (%q, %v)", plain, err)
	}
	if _, err := decrypt(record, "swapped.ref"); !errors.Is(err, ErrCiphertextAuthentication) {
		t.Fatalf("wrong-ref decrypt error = %v, want ErrCiphertextAuthentication", err)
	}
}
