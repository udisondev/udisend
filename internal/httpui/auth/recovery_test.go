package auth

import (
	"regexp"
	"strings"
	"testing"
)

func TestGenerateRecoveryCodes_ShapeAndUniqueness(t *testing.T) {
	t.Parallel()

	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Errorf("got %d codes, want %d", len(codes), RecoveryCodeCount)
	}

	pattern := regexp.MustCompile(`^[A-Z2-7]{4}-[A-Z2-7]{4}$`)
	seen := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		if !pattern.MatchString(c) {
			t.Errorf("code %q does not match XXXX-XXXX base32 pattern", c)
		}
		if _, dup := seen[c]; dup {
			t.Errorf("duplicate code %q within a single batch", c)
		}
		seen[c] = struct{}{}
	}
}

func TestGenerateRecoveryCodes_DistinctAcrossCalls(t *testing.T) {
	t.Parallel()

	a, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	overlap := 0
	first := make(map[string]struct{}, len(a))
	for _, c := range a {
		first[c] = struct{}{}
	}
	for _, c := range b {
		if _, ok := first[c]; ok {
			overlap++
		}
	}
	if overlap > 0 {
		t.Errorf("two batches overlap by %d codes — randomness suspicious", overlap)
	}
}

func TestHashRecoveryCode_VerifyRoundtrip(t *testing.T) {
	t.Parallel()

	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	first := codes[0]

	hash, err := HashRecoveryCode(first)
	if err != nil {
		t.Fatal(err)
	}

	ok, err := VerifyRecoveryCode(hash, first)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("verify(correct code) = false")
	}

	ok, err = VerifyRecoveryCode(hash, codes[1])
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("verify(other code) = true")
	}
}

func TestVerifyRecoveryCode_NormalizesUserInput(t *testing.T) {
	t.Parallel()

	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := HashRecoveryCode(codes[0])
	if err != nil {
		t.Fatal(err)
	}

	// Users frequently retype recovery codes lowercase or with stray
	// whitespace. Verification should accept these as long as the
	// underlying alphanumeric content matches.
	variants := []string{
		strings.ToLower(codes[0]),
		" " + codes[0] + " ",
		strings.ReplaceAll(codes[0], "-", ""),
	}
	for _, v := range variants {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			ok, err := VerifyRecoveryCode(hash, v)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Errorf("verify(%q) = false; should normalize and match", v)
			}
		})
	}
}
