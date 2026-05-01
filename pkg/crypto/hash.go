package crypto

import (
	"hash"

	"golang.org/x/crypto/blake2b"
)

// BLAKE2bSize is the byte length of a BLAKE2b-256 digest.
const BLAKE2bSize = 32

// BLAKE2b256 computes BLAKE2b-256(data).
func BLAKE2b256(data []byte) [BLAKE2bSize]byte {
	return blake2b.Sum256(data)
}

// BLAKE2bKeyed returns a keyed BLAKE2b-256 hasher (for MAC use). Keys up to
// 32 bytes are supported. Callers that hand BLAKE2b a long key will get an
// error from this constructor — that is preferred over silent truncation.
func BLAKE2bKeyed(key []byte) (hash.Hash, error) {
	return blake2b.New256(key)
}
