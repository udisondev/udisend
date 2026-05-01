// Package crypto provides project-wide wrappers around the few primitive
// operations udisend uses: ChaCha20-Poly1305 AEAD, BLAKE2b hashing, and
// HKDF-SHA-256. The wrappers are thin — their job is to enforce a single
// algorithm choice across the codebase and provide a friendlier API than
// the stdlib equivalents at the call site.
package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// AEADKeySize is the required key length in bytes for the AEAD constructors.
const AEADKeySize = chacha20poly1305.KeySize

// NonceSize is the per-message nonce length in bytes.
const NonceSize = chacha20poly1305.NonceSize

// ErrShortKey is returned when an AEAD key has the wrong length.
var ErrShortKey = errors.New("crypto: AEAD key must be 32 bytes")

// AEAD wraps ChaCha20-Poly1305 with a fixed key. The zero value is invalid;
// always construct via NewAEAD.
type AEAD struct {
	cipher AEADCipher
}

// AEADCipher is the minimal interface AEAD relies on. It matches stdlib's
// cipher.AEAD; a separate type lets tests substitute a fake without dragging
// the standard interface in.
type AEADCipher interface {
	Seal(dst, nonce, plaintext, additionalData []byte) []byte
	Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	NonceSize() int
	Overhead() int
}

// NewAEAD constructs an AEAD with the given 32-byte key.
func NewAEAD(key []byte) (*AEAD, error) {
	if len(key) != AEADKeySize {
		return nil, ErrShortKey
	}
	c, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: aead: %w", err)
	}
	return &AEAD{cipher: c}, nil
}

// Seal encrypts and authenticates plaintext, prepending a randomly generated
// 12-byte nonce to the returned ciphertext. The output is
//
//	nonce(12) | ciphertext+tag
//
// associatedData is bound into the tag and must be passed unchanged to Open.
func (a *AEAD) Seal(plaintext, associatedData []byte) ([]byte, error) {
	nonce := make([]byte, a.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: aead: nonce: %w", err)
	}
	out := make([]byte, len(nonce), len(nonce)+len(plaintext)+a.cipher.Overhead())
	copy(out, nonce)
	return a.cipher.Seal(out, nonce, plaintext, associatedData), nil
}

// SealWithNonce is the deterministic variant of Seal. Callers are responsible
// for ensuring nonce uniqueness — repeating a (key, nonce) pair breaks both
// confidentiality and authenticity. The output does NOT include the nonce.
func (a *AEAD) SealWithNonce(nonce, plaintext, associatedData []byte) ([]byte, error) {
	if len(nonce) != a.cipher.NonceSize() {
		return nil, fmt.Errorf("crypto: aead: nonce must be %d bytes", a.cipher.NonceSize())
	}
	return a.cipher.Seal(nil, nonce, plaintext, associatedData), nil
}

// Open decrypts a payload produced by Seal. The first 12 bytes are read as
// the nonce; the remainder as ciphertext+tag.
func (a *AEAD) Open(payload, associatedData []byte) ([]byte, error) {
	nonceSize := a.cipher.NonceSize()
	if len(payload) < nonceSize+a.cipher.Overhead() {
		return nil, errors.New("crypto: aead: payload too short")
	}
	nonce := payload[:nonceSize]
	ciphertext := payload[nonceSize:]
	plaintext, err := a.cipher.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return nil, fmt.Errorf("crypto: aead: open: %w", err)
	}
	return plaintext, nil
}

// OpenWithNonce decrypts a deterministic-nonce ciphertext produced by
// SealWithNonce.
func (a *AEAD) OpenWithNonce(nonce, ciphertext, associatedData []byte) ([]byte, error) {
	if len(nonce) != a.cipher.NonceSize() {
		return nil, fmt.Errorf("crypto: aead: nonce must be %d bytes", a.cipher.NonceSize())
	}
	plaintext, err := a.cipher.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return nil, fmt.Errorf("crypto: aead: open: %w", err)
	}
	return plaintext, nil
}
