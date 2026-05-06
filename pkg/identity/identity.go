// Package identity provides the cryptographic identity of a udisend user
// AND the Suite abstraction that lets external consumers plug in
// alternative crypto-suites (post-quantum, different DH curve, etc.).
//
// The reference Suite0x01 bundles Ed25519 (signing) + X25519 (key
// agreement) + ChaCha20-Poly1305 (AEAD, used by pkg/noise) +
// BLAKE2b (hashing). Identity is the concrete Suite0x01 keypair:
// derived deterministically from a 32-byte master seed; destination
// hash is the first 16 bytes of SHA-256(ed_pub‖x_pub).
//
// Suite-agnostic boundary types (pkg/dht, pkg/presence, pkg/signaling,
// pkg/webrtc) accept identity.PeerID instead of identity.Hash, and
// identity.Local / identity.Remote instead of *identity.Identity /
// identity.PublicIdentity. *identity.Identity satisfies Local;
// identity.PublicIdentity satisfies Remote; identity.Hash satisfies
// PeerID — so existing call sites using the concrete types continue
// to compile.
//
// Adding a new Suite (e.g. Suite0x02 with PQ-hybrid keys):
//
//  1. Implement the abstract interfaces (PeerID, Signer, Verifier,
//     KeyAgreement, Local, Remote).
//  2. Provide a Suite implementation with ID() returning a fresh byte
//     not yet registered in suiteRegistry.
//  3. registerSuite(YourSuite) in init(). LookupSuite picks it up.
//  4. Wire-format decoders that want to dispatch by suite-id call
//     LookupSuite(version-byte). Currently no decoder embeds a
//     separate suite-id; suite-aware wire formats are a future
//     extension (see ROADMAP Phase 11.5 scope-cut + Phase 12+).
package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/curve25519"
)

const (
	// SeedSize is the length of the master seed used to derive an Identity.
	SeedSize = 32
	// HashSize is the length of a destination hash in bytes.
	HashSize = 16
	// SignatureSize is the length of an Ed25519 signature in bytes.
	SignatureSize = ed25519.SignatureSize
	// PublicKeySize is the byte length of a marshalled PublicIdentity body
	// (excluding the 1-byte version prefix): ed25519_pub (32) + x25519_pub (32).
	PublicKeySize = ed25519.PublicKeySize + curve25519.PointSize

	// xKeyContext is the BLAKE2b personalisation string used to derive the
	// X25519 secret from the master seed. Changing this breaks compatibility.
	xKeyContext = "udisend-x25519-key"

	// privateMarshalVersion / publicMarshalVersion identify the wire layout.
	privateMarshalVersion byte = 0x01
	// PublicMarshalVersion is the wire-format version emitted by
	// PublicIdentity.MarshalBinary. Exported so adjacent packages
	// (e.g. pkg/presence) that inline the layout for hot-path encoding
	// stay in sync if it ever bumps — bumping here without updating
	// the inlined call sites would otherwise produce a silent drift.
	PublicMarshalVersion byte = 0x01
	publicMarshalVersion      = PublicMarshalVersion
)

// Errors returned by the package.
var (
	ErrInvalidSeed        = errors.New("identity: seed must be 32 bytes")
	ErrInvalidMarshal     = errors.New("identity: invalid marshalled identity")
	ErrUnknownVersion     = errors.New("identity: unknown marshal version")
	ErrInvalidPublicBlob  = errors.New("identity: invalid marshalled public identity")
	ErrInvalidSignatureSz = errors.New("identity: signature has unexpected length")
)

// Hash is a 16-byte destination hash that uniquely identifies a peer on the
// network. It is derived from the public keys; see PublicIdentity.DestinationHash.
type Hash [HashSize]byte

// Signature is a fixed-size Ed25519 signature.
type Signature [SignatureSize]byte

// Identity is a peer's full keypair: signing (Ed25519) plus key-exchange
// (X25519). Construct with Generate or FromSeed; never zero-construct.
type Identity struct {
	seed   [SeedSize]byte
	edPriv ed25519.PrivateKey
	xPriv  [curve25519.ScalarSize]byte
	xPub   [curve25519.PointSize]byte
}

// PublicIdentity is the public half of an Identity, safe to share with peers.
type PublicIdentity struct {
	EdPub ed25519.PublicKey
	XPub  [curve25519.PointSize]byte
}

// Generate returns a fresh Identity sourced from the given reader. Use
// crypto/rand.Reader in production and a deterministic reader in tests.
func Generate(r io.Reader) (*Identity, error) {
	var seed [SeedSize]byte
	if _, err := io.ReadFull(r, seed[:]); err != nil {
		return nil, fmt.Errorf("identity: read seed: %w", err)
	}
	return FromSeed(seed)
}

// FromSeed deterministically derives an Identity from the given 32-byte seed.
// The Ed25519 key uses the seed directly; the X25519 secret is derived via
// keyed BLAKE2b so the two keys are domain-separated.
func FromSeed(seed [SeedSize]byte) (*Identity, error) {
	id := &Identity{seed: seed}
	id.edPriv = ed25519.NewKeyFromSeed(seed[:])

	h, err := blake2b.New256([]byte(xKeyContext))
	if err != nil {
		return nil, fmt.Errorf("identity: derive x25519 key: %w", err)
	}
	if _, err := h.Write(seed[:]); err != nil {
		return nil, fmt.Errorf("identity: derive x25519 key: %w", err)
	}
	sum := h.Sum(nil)
	copy(id.xPriv[:], sum[:curve25519.ScalarSize])
	// X25519 clamping per RFC 7748 §5.
	id.xPriv[0] &= 248
	id.xPriv[31] &= 127
	id.xPriv[31] |= 64

	pub, err := curve25519.X25519(id.xPriv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("identity: derive x25519 pub: %w", err)
	}
	copy(id.xPub[:], pub)
	return id, nil
}

// Public returns the public half of the identity, safe to share.
func (i *Identity) Public() PublicIdentity {
	return PublicIdentity{
		EdPub: append(ed25519.PublicKey(nil), i.edPriv.Public().(ed25519.PublicKey)...),
		XPub:  i.xPub,
	}
}

// Seed exposes the derivation seed for callers that need to back up the key.
// Mutating the returned array does not affect the Identity.
func (i *Identity) Seed() [SeedSize]byte { return i.seed }

// Sign produces an Ed25519 signature over msg as a freshly-allocated
// byte slice (length identity.SignatureSize). Implements the Signer
// interface from suite.go; consumers that want the fixed-size form
// can copy into an identity.Signature.
func (i *Identity) Sign(msg []byte) []byte {
	return ed25519.Sign(i.edPriv, msg)
}

// AgreementPublic returns this identity's X25519 public key as a
// freshly-allocated 32-byte slice. Implements KeyAgreement.
func (i *Identity) AgreementPublic() []byte {
	out := make([]byte, len(i.xPub))
	copy(out, i.xPub[:])

	return out
}

// Agree performs an X25519 ECDH against the remote public key bytes
// and returns the 32-byte shared secret. Implements KeyAgreement.
// Callers MUST run the result through a KDF before using it as a
// session key — raw ECDH outputs are not uniform random.
func (i *Identity) Agree(remote []byte) ([]byte, error) {
	if len(remote) != curve25519.PointSize {
		return nil, fmt.Errorf("%w: agreement key length %d, want %d",
			ErrInvalidPublicBlob, len(remote), curve25519.PointSize)
	}
	shared, err := curve25519.X25519(i.xPriv[:], remote)
	if err != nil {
		return nil, fmt.Errorf("identity: x25519 ecdh: %w", err)
	}

	return shared, nil
}

// Remote returns the public-half of this identity as a Remote
// interface value. Implements Local.
func (i *Identity) Remote() Remote { return i.Public() }

// XPriv returns a copy of the X25519 secret. flynn/noise (consumed via
// pkg/noise) requires the raw 32-byte private scalar to drive its own
// CipherState construction; pkg/signaling bridges *Identity into a
// noise.StaticKeypair through this method. Prefer Agree (suite-agnostic
// ECDH) for any other use — XPriv is the documented escape hatch, not a
// general-purpose accessor.
func (i *Identity) XPriv() [curve25519.ScalarSize]byte { return i.xPriv }

// SharedSecret performs an X25519 ECDH against peer.XPub and returns the
// 32-byte shared secret. Callers MUST NOT use this raw output as a session
// key — pass it through a KDF (see pkg/crypto.HKDF).
//
// Deprecated: prefer Agree (Suite-agnostic).
func (i *Identity) SharedSecret(peer PublicIdentity) ([32]byte, error) {
	var out [32]byte
	shared, err := curve25519.X25519(i.xPriv[:], peer.XPub[:])
	if err != nil {
		return out, fmt.Errorf("identity: x25519 ecdh: %w", err)
	}
	copy(out[:], shared)

	return out, nil
}

// MarshalBinary encodes the full Identity (seed-based) for on-disk storage.
// The layout is:
//
//	version(1) | seed(32)
//
// Identity is stored as a seed; the public half is recomputed on load.
func (i *Identity) MarshalBinary() ([]byte, error) {
	out := make([]byte, 1+SeedSize)
	out[0] = privateMarshalVersion
	copy(out[1:], i.seed[:])
	return out, nil
}

// UnmarshalBinary decodes an Identity previously produced by MarshalBinary.
func (i *Identity) UnmarshalBinary(data []byte) error {
	if len(data) != 1+SeedSize {
		return ErrInvalidMarshal
	}
	if data[0] != privateMarshalVersion {
		return ErrUnknownVersion
	}
	var seed [SeedSize]byte
	copy(seed[:], data[1:])
	got, err := FromSeed(seed)
	if err != nil {
		return err
	}
	*i = *got
	return nil
}

// MarshalBinary encodes a PublicIdentity for transport. Layout:
//
//	version(1) | ed_pub(32) | x_pub(32)
func (p PublicIdentity) MarshalBinary() ([]byte, error) {
	if len(p.EdPub) != ed25519.PublicKeySize {
		return nil, ErrInvalidPublicBlob
	}
	out := make([]byte, 1+PublicKeySize)
	out[0] = publicMarshalVersion
	copy(out[1:1+ed25519.PublicKeySize], p.EdPub)
	copy(out[1+ed25519.PublicKeySize:], p.XPub[:])
	return out, nil
}

// UnmarshalBinary decodes a PublicIdentity produced by MarshalBinary.
func (p *PublicIdentity) UnmarshalBinary(data []byte) error {
	if len(data) != 1+PublicKeySize {
		return ErrInvalidPublicBlob
	}
	if data[0] != publicMarshalVersion {
		return ErrUnknownVersion
	}
	ed := make(ed25519.PublicKey, ed25519.PublicKeySize)
	copy(ed, data[1:1+ed25519.PublicKeySize])
	var x [curve25519.PointSize]byte
	copy(x[:], data[1+ed25519.PublicKeySize:])
	p.EdPub = ed
	p.XPub = x
	return nil
}

// DestinationHash returns the peer's address: SHA-256(ed_pub‖x_pub)[:16].
//
// Note: PeerID() returns the same value behind the suite-agnostic
// PeerID interface; new code should prefer PeerID.
func (p PublicIdentity) DestinationHash() Hash {
	var msg [PublicKeySize]byte
	copy(msg[:ed25519.PublicKeySize], p.EdPub)
	copy(msg[ed25519.PublicKeySize:], p.XPub[:])
	digest := sha256.Sum256(msg[:])
	var h Hash
	copy(h[:], digest[:HashSize])

	return h
}

// PeerID returns the peer's network address as a PeerID interface
// value. Equivalent to DestinationHash() but typed for use with the
// suite-agnostic Remote interface.
func (p PublicIdentity) PeerID() PeerID { return p.DestinationHash() }

// AgreementPublic returns this peer's X25519 public key as a
// freshly-allocated 32-byte slice. Implements Remote.
func (p PublicIdentity) AgreementPublic() []byte {
	out := make([]byte, len(p.XPub))
	copy(out, p.XPub[:])

	return out
}

// Marshal returns the wire-format encoding of this PublicIdentity
// (version(1) || ed_pub(32) || x_pub(32)). Implements Remote.
//
// Marshal panics on encode failure; PublicIdentity values produced by
// Suite0x01 (Generate / ParseRemote) are always well-formed, so the
// branch is unreachable in practice. A panic here indicates a bug
// (e.g. a caller hand-constructed a PublicIdentity with the wrong
// EdPub length).
func (p PublicIdentity) Marshal() []byte {
	blob, err := p.MarshalBinary()
	if err != nil {
		panic(fmt.Sprintf("identity: PublicIdentity.Marshal: %v", err))
	}

	return blob
}

// Verify checks an Ed25519 signature against this public key. Returns
// false for any malformed input — wrong public-key length, wrong
// signature length — rather than panicking.
func (p PublicIdentity) Verify(msg []byte, sig []byte) bool {
	if len(p.EdPub) != ed25519.PublicKeySize {
		return false
	}
	if len(sig) != SignatureSize {
		return false
	}

	return ed25519.Verify(p.EdPub, msg, sig)
}

// Compile-time assertions that the concrete identity types satisfy
// the suite-agnostic interfaces declared in suite.go.
var (
	_ Local  = (*Identity)(nil)
	_ Remote = PublicIdentity{}
	_ PeerID = Hash{}
)
