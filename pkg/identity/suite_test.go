package identity_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
)

// Compile-time assertion: Hash satisfies PeerID. Bytes() is added in
// hash.go alongside the Suite interfaces.
var _ identity.PeerID = identity.Hash{}

func TestSuite0x01_ID(t *testing.T) {
	t.Parallel()

	if got := identity.Suite0x01.ID(); got != 0x01 {
		t.Fatalf("Suite0x01.ID() = %#x, want 0x01", got)
	}
}

func TestSuite0x01_Generate_SignVerifyRoundTrip(t *testing.T) {
	t.Parallel()

	local, err := identity.Suite0x01.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if local == nil {
		t.Fatalf("Generate returned nil Local")
	}

	remote := local.Remote()
	if remote == nil {
		t.Fatalf("local.Remote() returned nil")
	}
	if remote.PeerID().Bytes() == (identity.Hash{}) {
		t.Fatalf("PeerID is zero hash")
	}

	msg := []byte("phase 11 suite roundtrip")
	sig := local.Sign(msg)
	if !remote.Verify(msg, sig) {
		t.Fatalf("Remote.Verify rejected a fresh signature")
	}

	if remote.Verify([]byte("tampered msg"), sig) {
		t.Fatalf("Verify accepted signature over different message")
	}

	tampered := append([]byte(nil), sig...)
	tampered[0] ^= 0x01
	if remote.Verify(msg, tampered) {
		t.Fatalf("Verify accepted tampered signature")
	}
}

func TestSuite0x01_KeyAgreement_Symmetric(t *testing.T) {
	t.Parallel()

	a, err := identity.Suite0x01.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("gen A: %v", err)
	}
	b, err := identity.Suite0x01.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("gen B: %v", err)
	}

	sharedAB, err := a.Agree(b.AgreementPublic())
	if err != nil {
		t.Fatalf("A.Agree(B.pub): %v", err)
	}
	sharedBA, err := b.Agree(a.AgreementPublic())
	if err != nil {
		t.Fatalf("B.Agree(A.pub): %v", err)
	}

	if !bytes.Equal(sharedAB, sharedBA) {
		t.Fatalf("ECDH not symmetric:\n A→B %x\n B→A %x", sharedAB, sharedBA)
	}
	if len(sharedAB) != 32 {
		t.Fatalf("shared secret length = %d, want 32", len(sharedAB))
	}
}

func TestSuite0x01_Generate_DeterministicFromSeed(t *testing.T) {
	t.Parallel()

	const seedByte = 0xAB
	seed1 := bytes.NewReader(bytes.Repeat([]byte{seedByte}, identity.SeedSize))
	seed2 := bytes.NewReader(bytes.Repeat([]byte{seedByte}, identity.SeedSize))

	l1, err := identity.Suite0x01.Generate(seed1)
	if err != nil {
		t.Fatalf("gen 1: %v", err)
	}
	l2, err := identity.Suite0x01.Generate(seed2)
	if err != nil {
		t.Fatalf("gen 2: %v", err)
	}

	if l1.Remote().PeerID().Bytes() != l2.Remote().PeerID().Bytes() {
		t.Fatalf("same seed → different PeerID")
	}
}

// PeerID derived through Suite0x01 must match the legacy
// PublicIdentity.DestinationHash() so the migration is wire-compatible.
func TestSuite0x01_PeerID_MatchesLegacyDestinationHash(t *testing.T) {
	t.Parallel()

	seedBytes := bytes.Repeat([]byte{0x42}, identity.SeedSize)

	legacyID, err := identity.Generate(bytes.NewReader(seedBytes))
	if err != nil {
		t.Fatalf("legacy Generate: %v", err)
	}
	wantHash := legacyID.Public().DestinationHash()

	suiteLocal, err := identity.Suite0x01.Generate(bytes.NewReader(seedBytes))
	if err != nil {
		t.Fatalf("Suite Generate: %v", err)
	}
	gotPeerID := suiteLocal.Remote().PeerID()

	if gotPeerID.Bytes() != wantHash {
		t.Fatalf("PeerID via Suite (%s) ≠ legacy DestinationHash (%s)", gotPeerID, wantHash)
	}
	if gotPeerID.String() != wantHash.String() {
		t.Fatalf("PeerID.String mismatch: suite=%q legacy=%q", gotPeerID.String(), wantHash.String())
	}
}

// Suite.ParseRemote round-trips Remote.Marshal: ship public-half,
// parse it back, derive same PeerID.
func TestSuite0x01_ParseRemote_RoundTrip(t *testing.T) {
	t.Parallel()

	local, err := identity.Suite0x01.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	original := local.Remote()
	blob := original.Marshal()
	if len(blob) == 0 {
		t.Fatalf("Marshal returned empty blob")
	}
	if blob[0] != identity.Suite0x01.ID() {
		t.Fatalf("Marshal blob[0] = %#x, want suite ID %#x", blob[0], identity.Suite0x01.ID())
	}

	parsed, err := identity.Suite0x01.ParseRemote(blob)
	if err != nil {
		t.Fatalf("ParseRemote: %v", err)
	}
	if parsed.PeerID().Bytes() != original.PeerID().Bytes() {
		t.Fatalf("PeerID mismatch after round-trip")
	}

	msg := []byte("round-trip verify")
	sig := local.Sign(msg)
	if !parsed.Verify(msg, sig) {
		t.Fatalf("parsed Remote rejected signature")
	}
}

func TestSuite0x01_ParseRemote_RejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		blob []byte
	}{
		{"empty", nil},
		{"single byte", []byte{0x01}},
		{"truncated", make([]byte, 1+identity.PublicKeySize-1)},
		{"oversize", make([]byte, 1+identity.PublicKeySize+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := identity.Suite0x01.ParseRemote(tt.blob); err == nil {
				t.Fatalf("ParseRemote(%q) accepted bad blob", tt.name)
			}
		})
	}
}

func TestSuite0x01_Generate_TruncatedSeedReturnsError(t *testing.T) {
	t.Parallel()

	short := bytes.NewReader(bytes.Repeat([]byte{0x01}, identity.SeedSize-1))
	_, err := identity.Suite0x01.Generate(short)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestSuite0x01_Agree_RejectsBadRemoteLength(t *testing.T) {
	t.Parallel()

	local, err := identity.Suite0x01.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}

	for _, n := range []int{0, 1, 31, 33, 64} {
		if _, err := local.Agree(make([]byte, n)); err == nil {
			t.Fatalf("Agree accepted remote of len %d", n)
		}
	}
}

func TestSuite0x01_AgreementPublic_LenAndStability(t *testing.T) {
	t.Parallel()

	local, err := identity.Suite0x01.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}

	pk1 := local.AgreementPublic()
	if len(pk1) != 32 {
		t.Fatalf("AgreementPublic length = %d, want 32", len(pk1))
	}

	pk2 := local.AgreementPublic()
	if !bytes.Equal(pk1, pk2) {
		t.Fatalf("AgreementPublic not stable across calls")
	}

	// Caller-mutation must not affect the next call (defensive copy).
	pk1[0] ^= 0xFF
	pk3 := local.AgreementPublic()
	if !bytes.Equal(pk2, pk3) {
		t.Fatalf("AgreementPublic returned alias to internal state")
	}
}

func TestHash_Bytes(t *testing.T) {
	t.Parallel()

	h := identity.Hash{0xDE, 0xAD, 0xBE, 0xEF}
	if got := h.Bytes(); got != h {
		t.Fatalf("Bytes() = %x, want %x", got, h)
	}
	if (identity.Hash{}).Bytes() != (identity.Hash{}) {
		t.Fatalf("zero hash Bytes mismatch")
	}
}

func TestLookupSuite_Known(t *testing.T) {
	t.Parallel()

	s, err := identity.LookupSuite(0x01)
	if err != nil {
		t.Fatalf("LookupSuite(0x01): %v", err)
	}
	if s == nil || s.ID() != 0x01 {
		t.Fatalf("LookupSuite(0x01) returned wrong suite: %v", s)
	}
}

func TestLookupSuite_Unknown(t *testing.T) {
	t.Parallel()

	for _, id := range []byte{0x00, 0x02, 0x10, 0xFF} {
		t.Run(byteHex(id), func(t *testing.T) {
			t.Parallel()

			_, err := identity.LookupSuite(id)
			if !errors.Is(err, identity.ErrUnknownSuite) {
				t.Fatalf("LookupSuite(%#x) err = %v, want ErrUnknownSuite", id, err)
			}
		})
	}
}

func byteHex(b byte) string {
	const hex = "0123456789abcdef"
	return "0x" + string([]byte{hex[b>>4], hex[b&0x0F]})
}
