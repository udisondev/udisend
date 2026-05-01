package identity_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
)

func mustGenerate(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return id
}

func TestGenerate_IndependentEachCall(t *testing.T) {
	t.Parallel()
	a := mustGenerate(t)
	b := mustGenerate(t)
	if a.Public().DestinationHash() == b.Public().DestinationHash() {
		t.Fatalf("two independent identities collided")
	}
}

func TestGenerate_PropagatesReaderError(t *testing.T) {
	t.Parallel()
	_, err := identity.Generate(iotest{}.Reader())
	if err == nil {
		t.Fatalf("expected error from short reader")
	}
}

func TestFromSeed_Deterministic(t *testing.T) {
	t.Parallel()
	var seed [identity.SeedSize]byte
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	a, err := identity.FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	if a.Public().DestinationHash() != b.Public().DestinationHash() {
		t.Fatalf("FromSeed must be deterministic")
	}
}

func TestSignVerify(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		signer  func() *identity.Identity
		verify  func() identity.PublicIdentity
		message []byte
		tamper  func([]byte) []byte
		want    bool
	}

	id := mustGenerate(t)
	other := mustGenerate(t)
	cases := []tc{
		{
			name:    "self verifies",
			signer:  func() *identity.Identity { return id },
			verify:  func() identity.PublicIdentity { return id.Public() },
			message: []byte("hello, udisend"),
			want:    true,
		},
		{
			name:    "wrong public key fails",
			signer:  func() *identity.Identity { return id },
			verify:  func() identity.PublicIdentity { return other.Public() },
			message: []byte("hello, udisend"),
			want:    false,
		},
		{
			name:    "tampered message fails",
			signer:  func() *identity.Identity { return id },
			verify:  func() identity.PublicIdentity { return id.Public() },
			message: []byte("hello, udisend"),
			tamper:  func(b []byte) []byte { c := append([]byte{}, b...); c[0] ^= 0x01; return c },
			want:    false,
		},
		{
			name:    "empty message verifies",
			signer:  func() *identity.Identity { return id },
			verify:  func() identity.PublicIdentity { return id.Public() },
			message: []byte{},
			want:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sig := c.signer().Sign(c.message)
			msg := c.message
			if c.tamper != nil {
				msg = c.tamper(msg)
			}
			got := c.verify().Verify(msg, sig)
			if got != c.want {
				t.Errorf("Verify = %v, want %v", got, c.want)
			}
		})
	}
}

func TestVerify_RejectsZeroPubKey(t *testing.T) {
	t.Parallel()
	id := mustGenerate(t)
	sig := id.Sign([]byte("x"))
	var empty identity.PublicIdentity
	if empty.Verify([]byte("x"), sig) {
		t.Fatalf("zero public identity must reject every signature")
	}
}

func TestDestinationHash_LengthAndStability(t *testing.T) {
	t.Parallel()
	id := mustGenerate(t)
	h1 := id.Public().DestinationHash()
	h2 := id.Public().DestinationHash()
	if h1 != h2 {
		t.Fatalf("destination hash must be stable")
	}
	if len(h1) != identity.HashSize {
		t.Fatalf("hash size = %d, want %d", len(h1), identity.HashSize)
	}
}

func TestMarshalBinary_Roundtrip(t *testing.T) {
	t.Parallel()
	id := mustGenerate(t)
	blob, err := id.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var got identity.Identity
	if err := got.UnmarshalBinary(blob); err != nil {
		t.Fatal(err)
	}
	if got.Public().DestinationHash() != id.Public().DestinationHash() {
		t.Fatalf("roundtrip dest hash mismatch")
	}
	sig := got.Sign([]byte("after roundtrip"))
	if !id.Public().Verify([]byte("after roundtrip"), sig) {
		t.Fatalf("signature from roundtripped identity should verify")
	}
}

func TestPublicIdentityMarshal_Roundtrip(t *testing.T) {
	t.Parallel()
	id := mustGenerate(t)
	blob, err := id.Public().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var got identity.PublicIdentity
	if err := got.UnmarshalBinary(blob); err != nil {
		t.Fatal(err)
	}
	if got.DestinationHash() != id.Public().DestinationHash() {
		t.Fatalf("public marshal roundtrip failed")
	}
	if !bytes.Equal(got.EdPub, id.Public().EdPub) {
		t.Fatalf("ed pub mismatch")
	}
}

func TestUnmarshal_RejectsTruncated(t *testing.T) {
	t.Parallel()
	id := mustGenerate(t)
	blob, err := id.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for length := range len(blob) {
		var got identity.Identity
		if err := got.UnmarshalBinary(blob[:length]); err == nil {
			t.Errorf("truncated blob len=%d unexpectedly accepted", length)
		}
	}
}

func TestUnmarshal_RejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	id := mustGenerate(t)
	blob, _ := id.MarshalBinary()
	bad := append([]byte{}, blob...)
	bad[0] = 0xFF
	var got identity.Identity
	if err := got.UnmarshalBinary(bad); !errors.Is(err, identity.ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
}

func TestPublicUnmarshal_RejectsUnknownVersion(t *testing.T) {
	t.Parallel()
	id := mustGenerate(t)
	blob, _ := id.Public().MarshalBinary()
	bad := append([]byte{}, blob...)
	bad[0] = 0x99
	var got identity.PublicIdentity
	if err := got.UnmarshalBinary(bad); !errors.Is(err, identity.ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
}

func TestSharedSecret_AgreesBothWays(t *testing.T) {
	t.Parallel()
	a := mustGenerate(t)
	b := mustGenerate(t)
	abShared, err := a.SharedSecret(b.Public())
	if err != nil {
		t.Fatal(err)
	}
	baShared, err := b.SharedSecret(a.Public())
	if err != nil {
		t.Fatal(err)
	}
	if abShared != baShared {
		t.Fatalf("ECDH should be symmetric")
	}
}

func TestSharedSecret_DifferentPeers_DifferentSecret(t *testing.T) {
	t.Parallel()
	a := mustGenerate(t)
	b := mustGenerate(t)
	c := mustGenerate(t)
	ab, _ := a.SharedSecret(b.Public())
	ac, _ := a.SharedSecret(c.Public())
	if ab == ac {
		t.Fatalf("ECDH against different peers must differ")
	}
}

func TestFingerprint_StableAndStructured(t *testing.T) {
	t.Parallel()
	var seed [identity.SeedSize]byte
	for i := range seed {
		seed[i] = 0x42
	}
	id, err := identity.FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	fp1 := id.Public().Fingerprint()
	fp2 := id.Public().Fingerprint()
	if fp1 != fp2 {
		t.Fatalf("fingerprint must be stable across calls")
	}
	groups := strings.Split(fp1, " ")
	if len(groups) != 12 {
		t.Fatalf("expected 12 groups, got %d (fp=%q)", len(groups), fp1)
	}
	for _, g := range groups {
		if len(g) != 5 {
			t.Fatalf("group %q must be 5 digits", g)
		}
		for _, r := range g {
			if r < '0' || r > '9' {
				t.Fatalf("non-digit char in fingerprint: %q", g)
			}
		}
	}
}

func TestFingerprint_DiffersBetweenIdentities(t *testing.T) {
	t.Parallel()
	a := mustGenerate(t)
	b := mustGenerate(t)
	if a.Public().Fingerprint() == b.Public().Fingerprint() {
		t.Fatalf("fingerprints should differ between random identities")
	}
}

func TestParseHash_Roundtrip(t *testing.T) {
	t.Parallel()
	id := mustGenerate(t)
	h := id.Public().DestinationHash()
	got, err := identity.ParseHash(h.String())
	if err != nil {
		t.Fatal(err)
	}
	if got != h {
		t.Fatalf("hash roundtrip mismatch")
	}
}

func TestParseHash_Errors(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"too-short",
		strings.Repeat("g", 32), // not hex
		strings.Repeat("0", 31), // wrong length
	}
	for _, c := range cases {
		if _, err := identity.ParseHash(c); err == nil {
			t.Errorf("ParseHash(%q) = nil, want error", c)
		}
	}
}

// iotest gives a Reader that always returns 0 bytes + EOF, useful for
// checking error propagation.
type iotest struct{}

func (iotest) Reader() io.Reader { return failingReader{} }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.EOF }
