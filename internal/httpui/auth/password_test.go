package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashPassphrase_Roundtrip(t *testing.T) {
	t.Parallel()

	encoded, err := HashPassphrase("hunter2-very-long-passphrase")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$") {
		t.Fatalf("encoded form unexpected: %q", encoded)
	}

	ok, err := VerifyPassphrase(encoded, "hunter2-very-long-passphrase")
	if err != nil {
		t.Fatalf("verify(correct): %v", err)
	}
	if !ok {
		t.Errorf("verify(correct) = false, want true")
	}

	ok, err = VerifyPassphrase(encoded, "wrong")
	if err != nil {
		t.Fatalf("verify(wrong): %v", err)
	}
	if ok {
		t.Errorf("verify(wrong) = true, want false")
	}
}

func TestHashPassphrase_RejectsEmpty(t *testing.T) {
	t.Parallel()

	if _, err := HashPassphrase(""); !errors.Is(err, ErrEmptyPassphrase) {
		t.Errorf("err = %v, want ErrEmptyPassphrase", err)
	}
}

func TestHashPassphrase_DistinctSalts(t *testing.T) {
	t.Parallel()

	a, err := HashPassphrase("same passphrase")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassphrase("same passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two hashes of the same passphrase produced identical output — salt is not random")
	}
}

func TestVerifyPassphrase_MalformedInputs(t *testing.T) {
	t.Parallel()

	good, err := HashPassphrase("seed-passphrase")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		encoded string
	}{
		{"empty", ""},
		{"wrong algo prefix", strings.Replace(good, "$argon2id$", "$argon2i$", 1)},
		{"missing version", strings.Replace(good, "$v=19$", "$", 1)},
		{"truncated — no hash field", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA"},
		{"bad base64 salt", "$argon2id$v=19$m=65536,t=3,p=4$!!!notbase64!!!$AAAA"},
		{"bad base64 hash", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$###"},
		{"non-numeric memory", "$argon2id$v=19$m=abc,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAA"},
		{"unsupported version", strings.Replace(good, "v=19", "v=99", 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ok, err := VerifyPassphrase(tt.encoded, "anything")
			if err == nil {
				t.Errorf("expected error for malformed encoded; got ok=%v err=nil", ok)
			}
			if ok {
				t.Errorf("ok=true on malformed encoded")
			}
		})
	}
}

// TestVerifyPassphrase_RejectsAbsurdParams covers the OOM amplifier: a
// corrupted (or attacker-controlled) PHC row with `m=4294967295` (~4 TiB)
// fed straight into argon2.IDKey crashes the next login. decodePHC MUST
// clamp the parameter triple to safe bounds before letting argon2 see
// them.
func TestVerifyPassphrase_RejectsAbsurdParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		encoded string
	}{
		{
			name:    "memory 4 TiB",
			encoded: "$argon2id$v=19$m=4294967295,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		{
			name:    "iterations 1e6",
			encoded: "$argon2id$v=19$m=65536,t=1000000,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		{
			name:    "parallelism 200",
			encoded: "$argon2id$v=19$m=65536,t=3,p=200$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
		{
			name:    "memory zero",
			encoded: "$argon2id$v=19$m=0,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ok, err := VerifyPassphrase(tt.encoded, "anything")
			if err == nil {
				t.Errorf("absurd params accepted (ok=%v err=nil) — argon2.IDKey would have been called", ok)
			}
			if ok {
				t.Errorf("ok=true with absurd params")
			}
		})
	}
}

func TestDefaultParams_MeetOWASP(t *testing.T) {
	t.Parallel()

	p := DefaultParams
	if p.Memory < 64*1024 {
		t.Errorf("Memory %d KiB < 64 MiB OWASP minimum", p.Memory)
	}
	if p.Iterations < 2 {
		t.Errorf("Iterations %d < 2 OWASP minimum", p.Iterations)
	}
	if p.Parallelism < 1 {
		t.Errorf("Parallelism %d < 1", p.Parallelism)
	}
	if p.KeyLen < 16 {
		t.Errorf("KeyLen %d < 16", p.KeyLen)
	}
	if p.SaltLen < 16 {
		t.Errorf("SaltLen %d < 16", p.SaltLen)
	}
}
