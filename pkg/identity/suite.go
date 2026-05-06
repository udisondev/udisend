package identity

import (
	"errors"
	"fmt"
	"io"
)

// Crypto-suite abstractions for Phase 11 reusability + crypto-agility.
//
// A Suite bundles a signature scheme, key-agreement scheme, AEAD, and
// hash function under a single algorithm-id byte. The id maps to the
// wire-format version-byte every signed payload carries (see
// p2p-messenger-design §"Crypto agility through identifiers"). Decoders
// look the suite up via LookupSuite at parse time, so a future
// Suite0x02 can be added without rewriting consumers.
//
// Suite0x01 is the reference implementation: Ed25519 + X25519 +
// ChaCha20-Poly1305 + BLAKE2b. It wraps the existing Identity /
// PublicIdentity types behind the abstract interfaces. Subsequent
// sub-stages (11.2-11.6) make pkg/{dht,noise,presence,signaling,webrtc}
// accept these interfaces in their public API so external consumers
// can plug in their own Suite implementation.

// PeerID is a stable, comparable network address. Implementations
// expose a fixed-size byte representation (16 bytes for Suite0x01)
// and a stable text form.
type PeerID interface {
	Bytes() [HashSize]byte
	String() string
}

// Signer produces signatures over arbitrary messages with the local
// signing key. The returned slice is freshly allocated and owned by
// the caller.
type Signer interface {
	Sign(msg []byte) []byte
}

// Verifier verifies a signature produced by a Signer. Returns false
// for any malformed input rather than panicking.
type Verifier interface {
	Verify(msg, sig []byte) bool
}

// KeyAgreement performs a Diffie-Hellman-like operation against a
// remote public key. The returned shared secret MUST be passed
// through a KDF before being used as a session key — raw outputs
// are not uniform random.
type KeyAgreement interface {
	// AgreementPublic returns this party's public key in the format
	// the suite uses on the wire (32 bytes for X25519). Implementations
	// MUST return a defensive copy.
	AgreementPublic() []byte

	// Agree performs ECDH against the remote public key and returns
	// the shared secret. Errors when the remote key is malformed.
	Agree(remote []byte) ([]byte, error)
}

// Local is the local peer's private identity: it can sign, perform
// key agreement, and expose its public-half (Remote) for sharing
// with peers.
type Local interface {
	Signer
	KeyAgreement
	// Remote returns the public-half of this identity as a Remote.
	// Calling Remote on the same Local always yields a value with
	// the same PeerID and Marshal output.
	Remote() Remote
}

// Remote is a peer's public identity as observed by us. It can verify
// the peer's signatures, expose a stable PeerID, hand out the peer's
// agreement-public key, and marshal back to wire bytes for transit.
type Remote interface {
	Verifier
	// PeerID returns the peer's network address.
	PeerID() PeerID
	// AgreementPublic returns the peer's public agreement-key bytes.
	// Local.Agree(remote.AgreementPublic()) computes the shared secret
	// between us and remote.
	AgreementPublic() []byte
	// Marshal returns the wire-format encoding of this Remote,
	// including the leading 1-byte suite/version identifier. Round-trip
	// via Suite.ParseRemote.
	Marshal() []byte
}

// Suite specifies a versioned crypto-suite. ID() returns the
// algorithm-id byte that maps to the wire-format version-byte.
//
// The package-level registry (see LookupSuite) currently knows only
// Suite0x01. Future suites register via init() in their own files.
type Suite interface {
	// ID returns the suite's algorithm-id byte (1..255). 0x00 is
	// reserved for "unspecified" and is never a valid suite.
	ID() byte

	// Generate derives a fresh local identity from the given reader.
	// In production pass crypto/rand.Reader; in tests a deterministic
	// reader produces reproducible identities.
	Generate(rand io.Reader) (Local, error)

	// ParseRemote decodes a wire-format public-half blob (as produced
	// by Remote.Marshal). The blob's leading version-byte MUST equal
	// Suite.ID(); otherwise ParseRemote returns ErrUnknownVersion.
	ParseRemote(blob []byte) (Remote, error)
}

// ErrUnknownSuite is returned by LookupSuite when no Suite is
// registered for the requested algorithm-id byte. Decoders that read
// a version-byte off the wire return ErrUnknownSuite when LookupSuite
// fails so callers can distinguish "we don't speak this protocol
// version" from "the message is malformed".
var ErrUnknownSuite = errors.New("identity: unknown crypto-suite")

// suiteRegistry maps algorithm-id byte → Suite. Populated by init()
// blocks of suite-implementation files (currently only suite01.go).
// Reads happen on the decode hot path; writes happen at init time
// only, so no lock is needed.
var suiteRegistry = map[byte]Suite{}

func registerSuite(s Suite) {
	if s == nil {
		panic("identity: registerSuite(nil)")
	}
	id := s.ID()
	if existing, ok := suiteRegistry[id]; ok {
		panic(fmt.Sprintf("identity: suite id %#x already registered as %T (new: %T)", id, existing, s))
	}
	suiteRegistry[id] = s
}

// LookupSuite returns the Suite registered under the given
// algorithm-id byte, or ErrUnknownSuite if none is registered.
func LookupSuite(id byte) (Suite, error) {
	s, ok := suiteRegistry[id]
	if !ok {
		return nil, fmt.Errorf("%w: 0x%02x", ErrUnknownSuite, id)
	}

	return s, nil
}
