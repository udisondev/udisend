package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters fixed for compatibility with mainstream authenticator
// apps (Google Authenticator, Authy, 1Password). Changing them silently
// would invalidate existing enrollments.
const (
	TOTPPeriodSeconds = 30
	TOTPDigits        = 6
	TOTPSecretBytes   = 20 // 160 bits, RFC 4226 §4 recommendation
	TOTPSkewSteps     = 1  // accept ±1 step (±30s) for clock skew
)

// GenerateTOTPSecret returns a fresh 20-byte HMAC-SHA1 secret and its
// unpadded base32 encoding (the canonical form authenticator apps accept).
func GenerateTOTPSecret() ([]byte, string, error) {
	return generateTOTPSecretFrom(rand.Reader)
}

func generateTOTPSecretFrom(r io.Reader) ([]byte, string, error) {
	secret := make([]byte, TOTPSecretBytes)
	if _, err := io.ReadFull(r, secret); err != nil {
		return nil, "", fmt.Errorf("auth: read totp secret: %w", err)
	}

	return secret, base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), nil
}

// DecodeTOTPSecret reverses the base32 encoding produced by
// GenerateTOTPSecret. Tolerant of padding and whitespace as user-typed
// secrets sometimes carry both.
func DecodeTOTPSecret(s string) ([]byte, error) {
	cleaned := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "-", ""))
	cleaned = strings.TrimRight(cleaned, "=")

	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(cleaned)
}

// VerifyTOTP returns true iff code matches the TOTP value derived from
// secret at any step within ±TOTPSkewSteps of now. Constant-time per-step.
//
// Replay protection (refusing the same step twice) is the caller's job —
// this function is stateless.
func VerifyTOTP(secret []byte, code string, now time.Time) bool {
	_, ok := VerifyTOTPStep(secret, code, now)

	return ok
}

// VerifyTOTPStep is the step-aware variant of VerifyTOTP. On a successful
// match it returns the matching step counter (unix-time / period); the
// caller persists it to refuse later attempts with the same step value
// (replay protection). The returned step is 0 when ok is false.
func VerifyTOTPStep(secret []byte, code string, now time.Time) (int64, bool) {
	if len(code) != TOTPDigits {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}

	step := uint64(now.Unix() / TOTPPeriodSeconds)
	matched := false
	matchedStep := int64(0)
	for offset := -TOTPSkewSteps; offset <= TOTPSkewSteps; offset++ {
		candidate := computeTOTP(secret, step+uint64(offset), TOTPDigits)
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			matched = true
			matchedStep = int64(step) + int64(offset)
		}
	}
	if !matched {
		return 0, false
	}

	return matchedStep, true
}

// CurrentTOTP returns the TOTP code valid at now for secret. Provided so
// CLI enrollment flows can confirm "your authenticator should show this
// number right now" without re-implementing the inner machinery.
func CurrentTOTP(secret []byte, now time.Time) string {
	return computeTOTP(secret, uint64(now.Unix()/TOTPPeriodSeconds), TOTPDigits)
}

// computeTOTP implements RFC 6238 over RFC 4226 (HOTP) with HMAC-SHA1.
// Returns a zero-padded decimal string of length digits.
func computeTOTP(secret []byte, counter uint64, digits int) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)

	mac := hmac.New(sha1.New, secret)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	// Dynamic truncation per RFC 4226 §5.3.
	offset := sum[len(sum)-1] & 0x0F
	value := (uint32(sum[offset])&0x7F)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])

	mod := uint32(1)
	for range digits {
		mod *= 10
	}

	return fmt.Sprintf("%0*d", digits, value%mod)
}

// OTPAuthURL builds an `otpauth://totp/...` URL suitable for QR-encoding or
// manual paste into an authenticator app. Per the de-facto Key URI spec.
func OTPAuthURL(secret []byte, issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", TOTPDigits))
	q.Set("period", fmt.Sprintf("%d", TOTPPeriodSeconds))

	return "otpauth://totp/" + label + "?" + q.Encode()
}
