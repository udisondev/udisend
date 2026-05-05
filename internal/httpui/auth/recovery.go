package auth

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"strings"
)

// RecoveryCodeCount is the number of one-time recovery codes generated per
// TOTP enrollment. 10 codes × 40 bits each = ample headroom for a
// single-user system.
const RecoveryCodeCount = 10

// recoveryCodeBytes is the raw entropy per code; 5 bytes encode to exactly
// 8 base32 chars (no padding) — formatted as XXXX-XXXX.
const recoveryCodeBytes = 5

// GenerateRecoveryCodes returns RecoveryCodeCount fresh codes in the
// human-friendly XXXX-XXXX form (base32 alphabet, uppercase). Show these
// once to the user; persist only the Argon2id hashes via HashRecoveryCode.
func GenerateRecoveryCodes() ([]string, error) {
	return generateRecoveryCodesFrom(rand.Reader)
}

func generateRecoveryCodesFrom(r io.Reader) ([]string, error) {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	out := make([]string, RecoveryCodeCount)
	buf := make([]byte, recoveryCodeBytes)
	for i := range out {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("auth: read recovery code: %w", err)
		}
		s := enc.EncodeToString(buf)
		out[i] = s[:4] + "-" + s[4:]
	}

	return out, nil
}

// HashRecoveryCode normalizes the code and stores it via the same Argon2id
// machinery used for passphrases. Recovery codes have ≥40 bits of entropy,
// so default cost is comfortable; verification loops over up to
// RecoveryCodeCount stored hashes per attempt and is rate-limited at the
// HTTP layer, so the worst-case latency is acceptable for an emergency path.
func HashRecoveryCode(code string) (string, error) {
	return HashPassphrase(normalizeRecoveryCode(code))
}

// VerifyRecoveryCode mirrors VerifyPassphrase but normalizes the user
// input first. Caller is responsible for marking the matching hash as
// consumed in storage so a code cannot be reused.
func VerifyRecoveryCode(encodedHash, code string) (bool, error) {
	return VerifyPassphrase(encodedHash, normalizeRecoveryCode(code))
}

// normalizeRecoveryCode upper-cases, strips whitespace and hyphens. We
// store and compare on the canonical form so users can re-enter a code in
// whatever shape their fingers produced.
func normalizeRecoveryCode(code string) string {
	s := strings.ToUpper(code)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")

	return s
}
