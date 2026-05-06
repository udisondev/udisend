package httpui

import (
	"crypto/rand"
	"testing"
)

func TestIdentityEncryptRoundtrip(t *testing.T) {
	t.Parallel()
	plain := []byte("super-secret seed bytes here")
	blob, err := encryptIdentity(plain, "passphrase-12345!", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptIdentity(blob, "passphrase-12345!")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Errorf("got %q, want %q", got, plain)
	}
}

func TestIdentityDecrypt_BadPassphrase(t *testing.T) {
	t.Parallel()
	blob, err := encryptIdentity([]byte("seed"), "right-passphrase!", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptIdentity(blob, "wrong-passphrase!"); err == nil {
		t.Fatalf("expected decryption failure")
	}
}

func TestIdentityDecrypt_BadFormat(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		nil,
		[]byte("short"),
		make([]byte, 100),
	}
	for _, c := range cases {
		if _, err := decryptIdentity(c, "x"); err == nil {
			t.Errorf("expected error for blob len=%d", len(c))
		}
	}
}

// TestIdentityDecrypt_RejectsOversizedBlob guards against a future
// import endpoint hooking decryptIdentity to attacker-controlled body
// without enforcing its own size cap. argon2 + AEAD.Open both allocate
// proportional to the payload — an unbounded blob is a trivial OOM.
func TestIdentityDecrypt_RejectsOversizedBlob(t *testing.T) {
	t.Parallel()
	blob := make([]byte, maxIdentityImportBlob+1)
	copy(blob, identityExportHeader)
	if _, err := decryptIdentity(blob, "x"); err == nil {
		t.Fatalf("oversized blob accepted; want ErrIdentityExportFormat")
	}
}
