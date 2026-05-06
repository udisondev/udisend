package identity_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
)

func TestPoWBits_NonNegativeForRealIdentity(t *testing.T) {
	t.Parallel()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bits := id.Public().PoWBits()
	if bits < 0 || bits > identity.HashSize*8 {
		t.Fatalf("PoWBits out of range: %d", bits)
	}
}

func TestGenerateWithPoW_FindsSeedForLowDifficulty(t *testing.T) {
	t.Parallel()
	id, err := identity.GenerateWithPoW(rand.Reader, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := id.Public().PoWBits(); got < 4 {
		t.Fatalf("got %d leading zero bits, want >= 4", got)
	}
}

func TestGenerateWithPoW_ZeroBitsEqualsGenerate(t *testing.T) {
	t.Parallel()
	id, err := identity.GenerateWithPoW(rand.Reader, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id == nil {
		t.Fatal("nil identity for 0-bit difficulty")
	}
}

func TestGenerateWithPoW_RejectsExcessiveDifficulty(t *testing.T) {
	t.Parallel()
	if _, err := identity.GenerateWithPoW(rand.Reader, identity.MaxPoWBits+1); err == nil {
		t.Fatal("expected error for difficulty > MaxPoWBits")
	}
}

func TestGenerateWithPoW_AbortsOnExhaustedReader(t *testing.T) {
	t.Parallel()
	// Bounded reader: enough bytes for one attempt at most. With 24-bit
	// difficulty, one attempt is overwhelmingly unlikely to succeed.
	r := bytes.NewReader(make([]byte, identity.SeedSize))
	_, err := identity.GenerateWithPoW(r, 24)
	if !errors.Is(err, identity.ErrPoWSearchAborted) {
		t.Fatalf("expected ErrPoWSearchAborted, got %v", err)
	}
}
