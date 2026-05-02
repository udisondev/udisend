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
	"errors"
	"fmt"

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
	SDPTypeOffer  byte = 0x01
	SDPTypeAnswer byte = 0x02
	SDPTypeBye    byte = 0x03
	// SDPTypeICE carries a single ICE candidate (JSON-encoded
	// RTCIceCandidateInit). Signed for parity with offer/answer — the
	// per-candidate signing cost is negligible and lets us treat the
	// signal pipe uniformly on both ends.
	SDPTypeICE byte = 0x04
)

// SignedSDP is a SDP description authenticated by the publisher.
type SignedSDP struct {
	Kind      byte // SDPTypeOffer / Answer / Bye
	SDP       string
	Signature identity.Signature
}

// Errors returned by the package.
var (
	ErrSDPDecode    = errors.New("webrtc: bad SDP envelope")
	ErrSDPSignature = errors.New("webrtc: bad SDP signature")
	ErrUnknownKind  = errors.New("webrtc: unknown SDP kind")
)

const sdpEnvelopeVersion byte = 0x01

// signingBytes returns the bytes covered by the Ed25519 signature.
func (s *SignedSDP) signingBytes() []byte {
	w := wire.NewWriter()
	w.WriteUint8(sdpEnvelopeVersion)
	w.WriteUint8(s.Kind)
	w.WriteString(s.SDP)
	return w.Bytes()
}

// Sign produces the signature over s with id's private key.
func (s *SignedSDP) Sign(id *identity.Identity) {
	s.Signature = id.Sign(s.signingBytes())
}

// Verify checks the signature against the supplied public identity.
func (s *SignedSDP) Verify(pub identity.PublicIdentity) error {
	if !pub.Verify(s.signingBytes(), s.Signature) {
		return ErrSDPSignature
	}
	return nil
}

// MarshalBinary encodes the envelope:
//
//	version(1) | kind(1) | sdp(uvarint+utf8) | signature(64)
func (s *SignedSDP) MarshalBinary() ([]byte, error) {
	if len(s.SDP) > MaxSDPSize {
		return nil, fmt.Errorf("%w: SDP too long", ErrSDPDecode)
	}
	w := wire.NewWriter()
	w.WriteUint8(sdpEnvelopeVersion)
	w.WriteUint8(s.Kind)
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
	sdp, err := b.ReadString(MaxSDPSize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}
	sig, err := b.ReadFixed(identity.SignatureSize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}
	if err := b.AssertEmpty(); err != nil {
		return fmt.Errorf("%w: %v", ErrSDPDecode, err)
	}
	s.Kind = kind
	s.SDP = sdp
	copy(s.Signature[:], sig)
	return nil
}
