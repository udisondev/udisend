package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
)

// Fingerprint returns a Signal-style human-verifiable safety number derived
// from the public identity. The output is 60 decimal digits arranged as 12
// groups of 5, separated by spaces. Two peers comparing fingerprints out of
// band (e.g., over a phone call or on paper) can detect a key-mismatch with
// overwhelming probability.
//
// The construction iteratively hashes the public-key blob 5200 times — the
// same iteration count Signal uses — so the cost is non-trivial for an
// attacker brute-forcing collisions but cheap to display once.
func (p PublicIdentity) Fingerprint() string {
	if len(p.EdPub) != 32 {
		return ""
	}
	const iterations = 5200
	var buf [PublicKeySize]byte
	copy(buf[:32], p.EdPub)
	copy(buf[32:], p.XPub[:])

	digest := sha256.Sum256(buf[:])
	for range iterations - 1 {
		next := sha256.New()
		next.Write(digest[:])
		next.Write(buf[:])
		digest = sha256.Sum256(next.Sum(nil))
	}

	// 12 groups × 5 digits, derived from 12 5-byte chunks of digest.
	// SHA-256 = 32 bytes; we use the first 12 chunks of 5 bits packed into
	// the trailing digits of decoded uint64s.
	var sb strings.Builder
	sb.Grow(12*5 + 11) // digits + spaces
	for i := range 12 {
		// pull 5 bytes (40 bits), mod 100000.
		var pad [8]byte
		copy(pad[3:], digest[i*2:i*2+5])
		v := binary.BigEndian.Uint64(pad[:]) % 100000
		if i > 0 {
			sb.WriteByte(' ')
		}
		// zero-pad to 5 digits.
		var grp [5]byte
		for j := 4; j >= 0; j-- {
			grp[j] = byte('0' + v%10)
			v /= 10
		}
		sb.Write(grp[:])
	}
	return sb.String()
}
