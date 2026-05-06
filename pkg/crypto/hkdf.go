package crypto

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// ErrEmptyOutput is returned by HKDF when length is zero.
var ErrEmptyOutput = errors.New("crypto: hkdf: length must be > 0")

// HKDF derives `length` bytes from secret using HKDF-SHA-256 with the given
// salt and context info. salt may be nil; in that case a zero-byte salt is
// used per RFC 5869.
func HKDF(secret, salt, info []byte, length int) ([]byte, error) {
	if length <= 0 {
		return nil, ErrEmptyOutput
	}
	r := hkdf.New(sha256.New, secret, salt, info)
	out := make([]byte, length)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("crypto: hkdf: %w", err)
	}
	return out, nil
}
