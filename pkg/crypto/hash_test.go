package crypto_test

import (
	"bytes"
	"testing"

	"github.com/udisondev/udisend/pkg/crypto"
)

func TestBLAKE2b256_StableForSameInput(t *testing.T) {
	t.Parallel()
	a := crypto.BLAKE2b256([]byte("hello"))
	b := crypto.BLAKE2b256([]byte("hello"))
	if a != b {
		t.Fatalf("BLAKE2b must be deterministic")
	}
}

func TestBLAKE2b256_DiffersForDifferentInput(t *testing.T) {
	t.Parallel()
	a := crypto.BLAKE2b256([]byte("hello"))
	b := crypto.BLAKE2b256([]byte("world"))
	if a == b {
		t.Fatalf("different inputs must hash differently")
	}
}

func TestBLAKE2bKeyed_AsMAC(t *testing.T) {
	t.Parallel()
	key := []byte("mac-key-mac-key-mac-key-mac-key!")
	h, err := crypto.BLAKE2bKeyed(key)
	if err != nil {
		t.Fatal(err)
	}
	h.Write([]byte("payload"))
	mac := h.Sum(nil)
	if len(mac) == 0 {
		t.Fatalf("MAC is empty")
	}

	// Verify with same key
	h2, _ := crypto.BLAKE2bKeyed(key)
	h2.Write([]byte("payload"))
	if !bytes.Equal(mac, h2.Sum(nil)) {
		t.Fatalf("same key+input should produce same MAC")
	}

	// Different key — different MAC
	h3, _ := crypto.BLAKE2bKeyed([]byte("different-key-different-keydiff!"))
	h3.Write([]byte("payload"))
	if bytes.Equal(mac, h3.Sum(nil)) {
		t.Fatalf("different keys must give different MACs")
	}
}
