package auth

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// rfc6238Seed is the seed used in RFC 6238 Appendix B test tables for SHA-1.
// ASCII "12345678901234567890" (20 bytes).
var rfc6238Seed = []byte("12345678901234567890")

func TestComputeTOTP_RFC6238Vectors(t *testing.T) {
	t.Parallel()

	// RFC 6238 Appendix B, SHA-1 column. Last 6 digits of the 8-digit
	// TOTP value (the RFC table shows 8 digits; we use 6 for compatibility
	// with Google Authenticator and similar apps).
	tests := []struct {
		name string
		unix int64
		want string
	}{
		{"t=59", 59, "287082"},
		{"t=1111111109", 1111111109, "081804"},
		{"t=1111111111", 1111111111, "050471"},
		{"t=1234567890", 1234567890, "005924"},
		{"t=2000000000", 2000000000, "279037"},
		{"t=20000000000", 20000000000, "353130"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			counter := uint64(tt.unix / TOTPPeriodSeconds)
			got := computeTOTP(rfc6238Seed, counter, TOTPDigits)
			if got != tt.want {
				t.Errorf("computeTOTP(t=%d) = %s, want %s", tt.unix, got, tt.want)
			}
		})
	}
}

func TestVerifyTOTP_AcceptsCurrent(t *testing.T) {
	t.Parallel()

	now := time.Unix(1234567890, 0)
	code := computeTOTP(rfc6238Seed, uint64(now.Unix()/TOTPPeriodSeconds), TOTPDigits)

	if !VerifyTOTP(rfc6238Seed, code, now) {
		t.Errorf("VerifyTOTP(current code) = false")
	}
}

func TestVerifyTOTP_AcceptsSkewWindow(t *testing.T) {
	t.Parallel()

	now := time.Unix(1234567890, 0)
	prev := computeTOTP(rfc6238Seed, uint64(now.Unix()/TOTPPeriodSeconds)-1, TOTPDigits)
	next := computeTOTP(rfc6238Seed, uint64(now.Unix()/TOTPPeriodSeconds)+1, TOTPDigits)

	if !VerifyTOTP(rfc6238Seed, prev, now) {
		t.Errorf("VerifyTOTP(prev step) = false, want true (±1 skew)")
	}
	if !VerifyTOTP(rfc6238Seed, next, now) {
		t.Errorf("VerifyTOTP(next step) = false, want true (±1 skew)")
	}
}

func TestVerifyTOTP_RejectsBeyondWindow(t *testing.T) {
	t.Parallel()

	now := time.Unix(1234567890, 0)
	tooOld := computeTOTP(rfc6238Seed, uint64(now.Unix()/TOTPPeriodSeconds)-2, TOTPDigits)
	tooNew := computeTOTP(rfc6238Seed, uint64(now.Unix()/TOTPPeriodSeconds)+2, TOTPDigits)

	if VerifyTOTP(rfc6238Seed, tooOld, now) {
		t.Errorf("VerifyTOTP(t-60s) = true, want false")
	}
	if VerifyTOTP(rfc6238Seed, tooNew, now) {
		t.Errorf("VerifyTOTP(t+60s) = true, want false")
	}
}

func TestVerifyTOTP_RejectsMalformedCodes(t *testing.T) {
	t.Parallel()

	now := time.Unix(1234567890, 0)
	tests := []string{
		"",
		"12345",      // too short
		"1234567",    // too long
		"abcdef",     // non-numeric
		"12 456",     // contains space
		"12345A",     // mixed
	}
	for _, code := range tests {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			if VerifyTOTP(rfc6238Seed, code, now) {
				t.Errorf("VerifyTOTP(%q) = true, want false", code)
			}
		})
	}
}

func TestGenerateTOTPSecret_DistinctRandom(t *testing.T) {
	t.Parallel()

	a, _, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != TOTPSecretBytes {
		t.Errorf("len(secret) = %d, want %d", len(a), TOTPSecretBytes)
	}
	if bytes.Equal(a, b) {
		t.Errorf("two GenerateTOTPSecret calls returned identical bytes")
	}
}

func TestGenerateTOTPSecret_Base32Roundtrip(t *testing.T) {
	t.Parallel()

	raw, b32, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTOTPSecret(b32)
	if err != nil {
		t.Fatalf("DecodeTOTPSecret: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Errorf("roundtrip mismatch")
	}
}

func TestOTPAuthURL_Format(t *testing.T) {
	t.Parallel()

	secret := bytes.Repeat([]byte{0xAB}, TOTPSecretBytes)
	url := OTPAuthURL(secret, "udisend", "alice@example.com")

	must := []string{
		"otpauth://totp/",
		"udisend:alice@example.com",
		"secret=",
		"issuer=udisend",
		"algorithm=SHA1",
		"digits=6",
		"period=30",
	}
	for _, s := range must {
		if !strings.Contains(url, s) {
			t.Errorf("URL missing %q\nfull: %s", s, url)
		}
	}
}
