// Package noise wraps github.com/flynn/noise with a pinned XK handshake
// matching the design doc: ChaCha20-Poly1305 AEAD, BLAKE2b hashing, X25519
// DH. Identity is supplied via pkg/identity — the static keypair maps
// directly to the X25519 half of the udisend identity.
//
// XK is the right pattern for this project:
//   - Initiator authenticates the responder via a known static key.
//   - Initiator's identity is encrypted to the responder during handshake
//     (zero-RTT identity hiding from passive observers / relays).
//   - Three message handshake; after message 3 both sides have a pair of
//     symmetric cipher states ready for application traffic.
package noise

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/flynn/noise"
	"golang.org/x/crypto/curve25519"

	"github.com/udisondev/udisend/pkg/identity"
)

// Errors returned by the package.
var (
	ErrHandshakeFailed = errors.New("noise: handshake failed")
	ErrSessionDone     = errors.New("noise: session already completed")
	ErrSessionNotReady = errors.New("noise: session handshake not finished")
	// ErrSessionExhausted is returned when Encrypt/Decrypt is called past
	// MaxMessagesPerKey. The session must be torn down and a fresh
	// handshake established — using the same key past this point would
	// approach the AEAD nonce-reuse boundary documented in the Noise
	// spec § 5.1 (NIST SP 800-38D recommends ≤2^32 invocations per
	// AES-GCM key; ChaCha20-Poly1305 is more forgiving but the same
	// budget keeps a single bound for both).
	ErrSessionExhausted = errors.New("noise: session message budget exhausted, rekey or reconnect")
)

// MaxMessagesPerKey caps the number of Encrypt or Decrypt operations a
// single Session may perform before refusing further use. 2^32 (~4
// billion) is far above realistic signaling-session traffic — a session
// hitting it is either a long-running buggy peer or an attacker —
// and far below 2^64 where flynn/noise's nonce counter actually wraps.
// Sessions exceeding the budget MUST be closed; the caller establishes
// a new Noise session for further traffic.
const MaxMessagesPerKey uint64 = 1 << 32

// cipherSuite is the project-pinned set: X25519 / ChaCha20-Poly1305 / BLAKE2b.
var cipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2b)

// Session encapsulates a single end of an XK handshake. After Done()
// returns true the application can call Encrypt / Decrypt.
//
// `done` is observed concurrently by the responder DoS-guard timer in
// pkg/signaling, so it is an atomic.Bool. The CipherState fields
// themselves are mutated only by handshake methods that are externally
// synchronised by the dispatcher / sendMu.
type Session struct {
	hs        *noise.HandshakeState
	send      *noise.CipherState // app → peer
	recv      *noise.CipherState // peer → app
	initiator bool
	done      atomic.Bool

	// sendCount and recvCount track post-handshake AEAD invocations on
	// each direction. Encrypt/Decrypt refuse past MaxMessagesPerKey to
	// keep each direction comfortably below the AEAD nonce-collision
	// boundary even in the absence of a Noise-spec rekey implementation.
	sendCount atomic.Uint64
	recvCount atomic.Uint64
}

// staticKeyFromIdentity pulls the X25519 keypair out of an identity. flynn
// noise expects the raw 32-byte secret — we already clamp during identity
// derivation, so it's safe to hand off directly.
func staticKeyFromIdentity(id *identity.Identity) noise.DHKey {
	priv := id.XPriv()
	pub, _ := curve25519.X25519(priv[:], curve25519.Basepoint)
	return noise.DHKey{Private: append([]byte(nil), priv[:]...), Public: pub}
}

// NewInitiator starts a handshake aimed at the given responder static
// public key.
func NewInitiator(id *identity.Identity, peer identity.PublicIdentity) (*Session, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cipherSuite,
		Random:        nil,
		Pattern:       noise.HandshakeXK,
		Initiator:     true,
		StaticKeypair: staticKeyFromIdentity(id),
		PeerStatic:    peer.XPub[:],
	})
	if err != nil {
		return nil, fmt.Errorf("noise: init initiator: %w", err)
	}
	return &Session{hs: hs, initiator: true}, nil
}

// NewResponder starts a handshake awaiting an initiator who already knows
// our static public key.
func NewResponder(id *identity.Identity) (*Session, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   cipherSuite,
		Pattern:       noise.HandshakeXK,
		Initiator:     false,
		StaticKeypair: staticKeyFromIdentity(id),
	})
	if err != nil {
		return nil, fmt.Errorf("noise: init responder: %w", err)
	}
	return &Session{hs: hs, initiator: false}, nil
}

// WriteMessage produces the next handshake message. Returns the bytes to
// send. After the final message both Encrypt and Decrypt become available.
func (s *Session) WriteMessage(payload []byte) ([]byte, error) {
	if s.send != nil {
		return nil, ErrSessionDone
	}
	out, cs1, cs2, err := s.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	s.maybeFinalize(cs1, cs2)
	return out, nil
}

// ReadMessage consumes the peer's next handshake message and returns its
// (decrypted, authenticated) payload, if any.
func (s *Session) ReadMessage(message []byte) ([]byte, error) {
	if s.send != nil {
		return nil, ErrSessionDone
	}
	out, cs1, cs2, err := s.hs.ReadMessage(nil, message)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHandshakeFailed, err)
	}
	s.maybeFinalize(cs1, cs2)
	return out, nil
}

// Done reports whether the handshake has completed and Encrypt / Decrypt
// are now safe to call. Reads atomically — the responder DoS-guard
// timer in pkg/signaling polls this from a separate goroutine.
func (s *Session) Done() bool { return s.done.Load() }

// PeerStatic returns the responder's static key as observed during the
// handshake. Initiators already know it; for responders it's the value
// the initiator authenticated themselves with — so callers can match it
// against an expected identity.
func (s *Session) PeerStatic() ([]byte, error) {
	pub := s.hs.PeerStatic()
	if len(pub) == 0 {
		return nil, ErrSessionNotReady
	}
	return append([]byte(nil), pub...), nil
}

// Encrypt seals plaintext under the post-handshake send cipher. Refuses
// past MaxMessagesPerKey (ErrSessionExhausted) so the AEAD nonce never
// approaches its collision boundary.
func (s *Session) Encrypt(plaintext, ad []byte) ([]byte, error) {
	if s.send == nil {
		return nil, ErrSessionNotReady
	}
	if s.sendCount.Load() >= MaxMessagesPerKey {
		return nil, ErrSessionExhausted
	}
	out, err := s.send.Encrypt(nil, ad, plaintext)
	if err != nil {
		return nil, fmt.Errorf("noise: encrypt: %w", err)
	}
	s.sendCount.Add(1)
	return out, nil
}

// Decrypt opens ciphertext using the post-handshake receive cipher.
// Refuses past MaxMessagesPerKey on the receive direction; an attacker
// cannot push the receiver past the limit without also crafting valid
// AEAD ciphertexts (which require the session key).
func (s *Session) Decrypt(ciphertext, ad []byte) ([]byte, error) {
	if s.recv == nil {
		return nil, ErrSessionNotReady
	}
	if s.recvCount.Load() >= MaxMessagesPerKey {
		return nil, ErrSessionExhausted
	}
	out, err := s.recv.Decrypt(nil, ad, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("noise: decrypt: %w", err)
	}
	s.recvCount.Add(1)
	return out, nil
}

func (s *Session) maybeFinalize(cs1, cs2 *noise.CipherState) {
	if cs1 == nil || cs2 == nil {
		return
	}
	// flynn/noise returns (cs1, cs2) where cs1 = initiator-send,
	// cs2 = responder-send. Each side picks its own halves.
	if s.initiator {
		s.send, s.recv = cs1, cs2
	} else {
		s.send, s.recv = cs2, cs1
	}
	// Publish readiness AFTER the cipher states are visible so any
	// goroutine that observes Done()==true also sees a populated send/recv.
	s.done.Store(true)
}
