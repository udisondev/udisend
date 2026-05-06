package crypto_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/udisondev/udisend/pkg/crypto"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, crypto.AEADKeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestAEAD_RoundTrip(t *testing.T) {
	t.Parallel()
	a, err := crypto.NewAEAD(mustKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("hello, world")
	ad := []byte("metadata-v1")
	ct, err := a.Seal(plaintext, ad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, plaintext) {
		t.Fatalf("ciphertext leaks plaintext")
	}
	got, err := a.Open(ct, ad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestAEAD_OpenRejectsTamper(t *testing.T) {
	t.Parallel()
	a, _ := crypto.NewAEAD(mustKey(t))
	ct, err := a.Seal([]byte("payload"), []byte("ad"))
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(b []byte, idx int) []byte {
		c := append([]byte{}, b...)
		c[idx] ^= 0x01
		return c
	}
	for idx := range ct {
		if _, err := a.Open(mutate(ct, idx), []byte("ad")); err == nil {
			t.Errorf("tampered byte %d unexpectedly accepted", idx)
		}
	}
}

func TestAEAD_RejectsWrongAD(t *testing.T) {
	t.Parallel()
	a, _ := crypto.NewAEAD(mustKey(t))
	ct, _ := a.Seal([]byte("payload"), []byte("ad-1"))
	if _, err := a.Open(ct, []byte("ad-2")); err == nil {
		t.Fatalf("Open with mismatched AD must fail")
	}
}

func TestAEAD_KeySizeValidated(t *testing.T) {
	t.Parallel()
	if _, err := crypto.NewAEAD(make([]byte, 5)); !errors.Is(err, crypto.ErrShortKey) {
		t.Fatalf("err = %v, want ErrShortKey", err)
	}
}

func TestAEAD_DeterministicNonce(t *testing.T) {
	t.Parallel()
	a, _ := crypto.NewAEAD(mustKey(t))
	nonce := make([]byte, crypto.NonceSize)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	ct1, err := a.SealWithNonce(nonce, []byte("hello"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ct2, err := a.SealWithNonce(nonce, []byte("hello"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ct1, ct2) {
		t.Fatalf("deterministic nonce must produce identical ciphertext")
	}
	pt, err := a.OpenWithNonce(nonce, ct1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "hello" {
		t.Fatalf("got %q", pt)
	}
}
