package identity

import (
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
)

// Suite0x01 is the reference crypto-suite registered for algorithm-id
// byte 0x01: Ed25519 signatures, X25519 key-agreement,
// ChaCha20-Poly1305 AEAD, BLAKE2b hashing. It is the only suite
// shipped with udisend.
//
// Suite0x01 wraps the existing Identity / PublicIdentity types as
// adapters that satisfy the abstract Local / Remote interfaces. The
// adapters are thin: they own no state beyond the wrapped concrete
// value. Sub-stage 11.2 will collapse the adapters by making Identity
// itself satisfy Local directly (breaking change to Sign / Verify
// signatures).
var Suite0x01 Suite = suite01{}

func init() {
	registerSuite(Suite0x01)
}

type suite01 struct{}

func (suite01) ID() byte { return PublicMarshalVersion }

func (suite01) Generate(r io.Reader) (Local, error) {
	id, err := Generate(r)
	if err != nil {
		return nil, err
	}

	return localV1{id: id}, nil
}

func (suite01) ParseRemote(blob []byte) (Remote, error) {
	var p PublicIdentity
	if err := p.UnmarshalBinary(blob); err != nil {
		return nil, err
	}

	return remoteV1{p: p}, nil
}

// localV1 is the Suite0x01 adapter for *Identity. The concrete
// pointer is preserved so consumers needing escape-hatch access
// (e.g. pkg/noise reaching for raw X25519 priv) can type-assert.
type localV1 struct {
	id *Identity
}

// Identity returns the underlying *Identity pointer. This is a
// Suite0x01-specific escape hatch for code that hasn't yet been
// migrated to the abstract Local interface (e.g. pkg/noise builds
// its noise.DHKey directly from xPriv). Suite0x02 and later will
// not expose this method.
func (l localV1) Identity() *Identity { return l.id }

func (l localV1) Sign(msg []byte) []byte {
	sig := l.id.Sign(msg)
	out := make([]byte, SignatureSize)
	copy(out, sig[:])

	return out
}

func (l localV1) AgreementPublic() []byte {
	pub := l.id.xPub
	out := make([]byte, len(pub))
	copy(out, pub[:])

	return out
}

func (l localV1) Agree(remote []byte) ([]byte, error) {
	if len(remote) != curve25519.PointSize {
		return nil, fmt.Errorf("%w: agreement key length %d, want %d",
			ErrInvalidPublicBlob, len(remote), curve25519.PointSize)
	}

	var rpub [curve25519.PointSize]byte
	copy(rpub[:], remote)
	shared, err := l.id.SharedSecret(PublicIdentity{XPub: rpub})
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(shared))
	copy(out, shared[:])

	return out, nil
}

func (l localV1) Remote() Remote {
	return remoteV1{p: l.id.Public()}
}

// remoteV1 is the Suite0x01 adapter for PublicIdentity.
type remoteV1 struct {
	p PublicIdentity
}

// PublicIdentity returns the underlying concrete value. Like
// localV1.Identity, this is a Suite0x01-specific escape hatch.
func (r remoteV1) PublicIdentity() PublicIdentity { return r.p }

func (r remoteV1) Verify(msg, sig []byte) bool {
	if len(sig) != SignatureSize {
		return false
	}
	var s Signature
	copy(s[:], sig)

	return r.p.Verify(msg, s)
}

func (r remoteV1) PeerID() PeerID {
	return r.p.DestinationHash()
}

func (r remoteV1) AgreementPublic() []byte {
	out := make([]byte, len(r.p.XPub))
	copy(out, r.p.XPub[:])

	return out
}

func (r remoteV1) Marshal() []byte {
	blob, err := r.p.MarshalBinary()
	if err != nil {
		// Suite0x01.Generate / ParseRemote both produce well-formed
		// PublicIdentity values (EdPub always 32 bytes), so MarshalBinary
		// cannot fail in practice. Returning nil here would mask a real
		// invariant violation — panic instead so the bug surfaces.
		panic(fmt.Sprintf("identity: Suite0x01 Remote.Marshal: %v", err))
	}

	return blob
}
