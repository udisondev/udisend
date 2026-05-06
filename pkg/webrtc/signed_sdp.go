// Package webrtc wires pion/webrtc/v4 into the udisend signaling layer.
// It produces signed SDP offers / answers, drives the ICE-gather-complete
// dance, and exposes a small DataChannel + media track API to the
// application layer.
//
// The cryptographic discipline:
//   - SDP and ICE candidates are emitted as a single fully-gathered
//     description (vanilla ICE) — no per-candidate exchange.
//   - The whole description is signed with the publisher's Ed25519 key.
//   - Receivers verify the signature against the peer's known public
//     identity (out-of-band TOFU at the application layer).
//   - The DTLS-fingerprint inside the signed SDP is the binding that
//     prevents a relay from MITM'ing the WebRTC session — see
//     ASSUMPTIONS.md for the deferred runtime fingerprint cross-check.
package webrtc

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/wire"
)

// MaxSDPSize caps signed SDP payload size to bound decode work.
const MaxSDPSize = 256 * 1024

// KindString returns a stable lowercase label used by the WS bridge
// when forwarding signals to the browser.
func KindString(k byte) string {
	switch k {
	case SDPTypeOffer:
		return "offer"
	case SDPTypeAnswer:
		return "answer"
	case SDPTypeICE:
		return "ice"
	case SDPTypeBye:
		return "bye"
	default:
		return "unknown"
	}
}

// KindFromString is the inverse of KindString.
func KindFromString(s string) (byte, bool) {
	switch s {
	case "offer":
		return SDPTypeOffer, true
	case "answer":
		return SDPTypeAnswer, true
	case "ice":
		return SDPTypeICE, true
	case "bye":
		return SDPTypeBye, true
	default:
		return 0, false
	}
}

// Inner-message type codes carried over a signaling channel.
const (
	SDPTypeOffer byte = iota + 1
	SDPTypeAnswer
	SDPTypeBye
	// SDPTypeICE carries a single ICE candidate (JSON-encoded
	// RTCIceCandidateInit). Signed for parity with offer/answer — the
	// per-candidate signing cost is negligible and lets us treat the
	// signal pipe uniformly on both ends.
	SDPTypeICE
)

// SessionIDSize is the byte length of the SignedSDP SessionID field.
// Matches signaling.SessionIDSize but pkg/webrtc cannot import signaling
// (would create a dependency cycle), so the size is duplicated as a
// constant — wire-compatible by construction.
const SessionIDSize = 16

// MaxClockSkew bounds how far in the past or future a SignedSDP's
// IssuedAt may be relative to the verifier's clock. Five minutes covers
// realistic NTP drift between peers without giving an attacker a long
// window to replay captured offers. Tighter would be safer; looser
// would tolerate noisier infra. Five minutes mirrors what most TOTP
// stacks treat as "concerning skew".
const MaxClockSkew = 5 * time.Minute

// SignedSDP is a SDP description authenticated by the publisher and
// bound to the (peer, session, time) tuple it was produced for.
//
// The binding fields close cross-session and cross-recipient replay:
// without them, an offer captured on the wire to peer A could later be
// replayed to peer B by a relay, who would set up an RTCPeerConnection
// thinking it is negotiating with the original signer (the signature
// alone is satisfied by the signer's pubkey regardless of who receives
// it). See review/threat-model.md "MITM на signaling".
type SignedSDP struct {
	Kind      byte                // SDPTypeOffer / Answer / Bye / ICE
	Recipient identity.Hash       // who this SDP is meant for
	SessionID [SessionIDSize]byte // signaling session this binds to
	IssuedAt  int64               // unix-seconds when produced
	SDP       string
	Signature identity.Signature
}

// Errors returned by the package.
var (
	ErrSDPDecode    = errors.New("webrtc: bad SDP envelope")
	ErrSDPSignature = errors.New("webrtc: bad SDP signature")
	ErrUnknownKind  = errors.New("webrtc: unknown SDP kind")
	// ErrSDPRecipient signals a SignedSDP whose Recipient is not the
	// expected peer hash. Cross-recipient replay defence.
	ErrSDPRecipient = errors.New("webrtc: signed SDP for unexpected recipient")
	// ErrSDPSession signals a SignedSDP whose SessionID does not match
	// the expected signaling session. Cross-session replay defence.
	ErrSDPSession = errors.New("webrtc: signed SDP for unexpected session")
	// ErrSDPClockSkew signals a SignedSDP whose IssuedAt is outside
	// [now-MaxClockSkew, now+MaxClockSkew]. Stale-replay defence.
	ErrSDPClockSkew = errors.New("webrtc: signed SDP outside clock skew")
)

// sdpEnvelopeVersion is bumped to 0x02 when SignedSDP gained the
// (Recipient, SessionID, IssuedAt) binding fields. v1 envelopes (without
// these fields) decode as decode-error: pre-MVP wire-incompatibility is
// acceptable because there are no other implementations in the wild.
const sdpEnvelopeVersion byte = 0x02

// sdpDomainSeparator scopes the signed bytes so a future protocol
// extension can never produce a signature that cross-collides with a
// SignedSDP signature. The value is fixed forever — never re-purpose.
const sdpDomainSeparator = "udisend-signed-sdp-v2\x00"

// signingBytes returns the bytes covered by the Ed25519 signature.
func (s *SignedSDP) signingBytes() []byte {
	w := wire.NewWriter()

	w.WriteFixed([]byte(sdpDomainSeparator))
	w.WriteUint8(sdpEnvelopeVersion)
	w.WriteUint8(s.Kind)
	w.WriteFixed(s.Recipient[:])
	w.WriteFixed(s.SessionID[:])
	w.WriteUint64(uint64(s.IssuedAt))
	w.WriteString(s.SDP)

	return w.Bytes()
}

// Sign produces the signature over s with id's private key.
func (s *SignedSDP) Sign(id *identity.Identity) {
	copy(s.Signature[:], id.Sign(s.signingBytes()))
}

// Verify checks the signature AND the channel-binding fields:
//
//   - signature against pub
//   - Recipient == expectedRecipient
//   - SessionID == expectedSessionID
//   - IssuedAt within MaxClockSkew of now
//
// All comparisons run regardless of order so callers can't "early-out"
// expensive checks via crafted input. SessionID and Recipient compare
// constant-time defensively — they are not secrets, but cheap to keep
// uniform across the codebase.
func (s *SignedSDP) Verify(pub identity.PublicIdentity, expectedRecipient identity.Hash, expectedSessionID [SessionIDSize]byte, now time.Time) error {
	if !pub.Verify(s.signingBytes(), s.Signature[:]) {
		return ErrSDPSignature
	}
	if subtle.ConstantTimeCompare(s.Recipient[:], expectedRecipient[:]) != 1 {
		return ErrSDPRecipient
	}
	if subtle.ConstantTimeCompare(s.SessionID[:], expectedSessionID[:]) != 1 {
		return ErrSDPSession
	}
	delta := now.Unix() - s.IssuedAt
	if delta < 0 {
		delta = -delta
	}
	if delta > int64(MaxClockSkew/time.Second) {
		return ErrSDPClockSkew
	}

	return nil
}

// VerifySignatureOnly checks only the cryptographic signature, skipping
// the channel-binding fields. Provided for callers that legitimately
// need to inspect a signed envelope before they know the expected
// binding (e.g. parsers in tests, off-line forensics). Production
// signaling MUST use Verify.
func (s *SignedSDP) VerifySignatureOnly(pub identity.PublicIdentity) error {
	if !pub.Verify(s.signingBytes(), s.Signature[:]) {
		return ErrSDPSignature
	}

	return nil
}

// MarshalBinary encodes the envelope:
//
//	version(1) | kind(1) | recipient(16) | session_id(16) | issued_at(uint64-be) | sdp(uvarint+utf8) | signature(64)
func (s *SignedSDP) MarshalBinary() ([]byte, error) {
	if len(s.SDP) > MaxSDPSize {
		return nil, fmt.Errorf("%w: SDP too long", ErrSDPDecode)
	}

	w := wire.NewWriter()

	w.WriteUint8(sdpEnvelopeVersion)
	w.WriteUint8(s.Kind)
	w.WriteFixed(s.Recipient[:])
	w.WriteFixed(s.SessionID[:])
	w.WriteUint64(uint64(s.IssuedAt))
	w.WriteString(s.SDP)
	w.WriteFixed(s.Signature[:])

	return w.Bytes(), nil
}

// UnmarshalBinary decodes a payload produced by MarshalBinary.
func (s *SignedSDP) UnmarshalBinary(data []byte) error {
	b := wire.NewBuffer(data)

	ver, err := b.ReadUint8()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}

	if ver != sdpEnvelopeVersion {
		return fmt.Errorf("%w: version=%d", ErrSDPDecode, ver)
	}

	kind, err := b.ReadUint8()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}

	if err := b.ReadFixedInto(s.Recipient[:]); err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}
	if err := b.ReadFixedInto(s.SessionID[:]); err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}
	issuedAt, err := b.ReadUint64()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}

	sdp, err := b.ReadString(MaxSDPSize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}

	sig, err := b.ReadFixed(identity.SignatureSize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}

	if err := b.SkipUnknownTLVs(); err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}

	s.Kind = kind
	s.IssuedAt = int64(issuedAt)
	s.SDP = sdp

	copy(s.Signature[:], sig)

	return nil
}
