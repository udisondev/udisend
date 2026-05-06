// Package signaling sets up encrypted bidirectional channels between two
// peers identified by their destination hashes. It rides on top of the DHT
// transport (sharing the same UDP socket: pkg/signaling.Service satisfies
// dht.Extension and registers itself via dht.Config.Extension) and
// uses Noise XK for the cryptographic handshake. Once a Channel is open,
// callers can send arbitrary bytes — typically WebRTC SDP/ICE payloads
// during session negotiation, or any application-level message in tests.
//
// Wire envelope (sits inside a wire frame of outer opcode 0x10, which is
// the embedder-range opcode pkg/signaling owns):
//
//	version(1) | recipient(16) | sender(16) | session_id(16) | inner_type(1)
//	  | inner_payload(uvarint+bytes)
//
// inner_type values: see InnerType*.
package signaling

import (
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/wire"
)

// Wire format version. v2 carries the `hops` byte for hop-by-hop relay
// loop-prevention. v1 envelopes (no hops counter) are rejected outright
// to prevent attackers from claiming v1 format and having us relay
// indefinitely.
const (
	envelopeVersion byte = 0x02

	// MaxHops bounds how many relay hops an envelope may traverse before
	// being dropped (hop-by-hop signaling routing).
	MaxHops byte = 8

	// frameTypeRelay is the outer wire-frame opcode for envelope frames.
	// It sits in the embedder range (>= 0x10) reserved by pkg/dht for
	// multiplexed protocols; pkg/dht never interprets this opcode itself
	// and dispatches matching frames to Config.Extension. Defined here
	// so signaling owns its own opcode space — see Phase 12.
	frameTypeRelay byte = 0x10
)

// SessionIDSize is the length of a session identifier.
const SessionIDSize = 16

// SessionID identifies a single signaling session between two peers.
type SessionID [SessionIDSize]byte

// NewSessionID returns a fresh random session id. Returns an error on
// RNG failure — a zero session id would collide across in-flight
// signaling sessions and break per-peer demux. pkg/ MUST NOT panic on
// operational failures (CLAUDE.md): callers receive the error and
// decide whether to refuse the session.
func NewSessionID() (SessionID, error) {
	var s SessionID
	if _, err := rand.Read(s[:]); err != nil {
		return SessionID{}, fmt.Errorf("signaling: new session id: %w", err)
	}

	return s, nil
}

// Inner-payload type codes.
//
// The 0x06–0x08 range carries node-to-node WebRTC mesh handshake.
// Payloads are encrypted under the same Noise XK session as InnerData —
// the discriminator is at the envelope layer so the mesh-handler at
// network.Node can dispatch without peeking inside ciphertext. Adding
// new InnerType codes is wire-format-additive: older builds either
// decode them to InnerType=N and surface as "unknown inner type" Debug
// logs (forward-compatible).
const (
	InnerHelloInit     byte = 0x01 // Noise XK message 1
	InnerHelloResp     byte = 0x02 // Noise XK message 2
	InnerHelloFinal    byte = 0x03 // Noise XK message 3
	InnerData          byte = 0x04 // encrypted application data
	InnerBye           byte = 0x05 // graceful close
	InnerMeshOffer     byte = 0x06 // node-mesh WebRTC SDP offer (encrypted)
	InnerMeshAnswer    byte = 0x07 // node-mesh WebRTC SDP answer (encrypted)
	InnerMeshCandidate byte = 0x08 // node-mesh ICE candidate (encrypted)
)

// IsMeshInner reports whether kind is one of the InnerMesh* codes.
// Used by Channel.SendMesh to refuse non-mesh payloads.
func IsMeshInner(kind byte) bool {
	switch kind {
	case InnerMeshOffer, InnerMeshAnswer, InnerMeshCandidate:
		return true
	default:
		return false
	}
}

// MaxInnerSize caps the inner payload to bound decode work.
const MaxInnerSize = 64 * 1024

// Envelope is the on-wire representation of one signaling packet.
//
// Hops counts how many relay forwards this envelope has traversed. The
// initiating peer sets it to 0; each forwarder increments before sending
// onwards. Envelopes with Hops >= MaxHops are dropped by relays to break
// loops. v1 envelopes (no Hops field) decode as Hops=0.
type Envelope struct {
	Recipient identity.Hash
	Sender    identity.Hash
	SessionID SessionID
	Hops      byte
	InnerType byte
	Payload   []byte
}

// Errors surfaced by Decode.
var (
	ErrEnvelope     = errors.New("signaling: invalid envelope")
	ErrUnknownInner = errors.New("signaling: unknown inner type")
)

// Encode wraps the envelope into a frameTypeRelay wire frame. Uses
// wire.NewFrameWriter so frame header and body share one allocation.
func (e *Envelope) Encode() ([]byte, error) {
	if len(e.Payload) > MaxInnerSize {
		return nil, fmt.Errorf("%w: payload too large", ErrEnvelope)
	}
	const fixed = 1 + identity.HashSize*2 + SessionIDSize + 1 + 1 + 4
	w := wire.NewFrameWriter(fixed + len(e.Payload))
	w.WriteUint8(envelopeVersion)
	w.WriteFixed(e.Recipient[:])
	w.WriteFixed(e.Sender[:])
	w.WriteFixed(e.SessionID[:])
	w.WriteUint8(e.Hops)
	w.WriteUint8(e.InnerType)
	w.WriteBytes(e.Payload)
	return wire.FinishFrame(w, frameTypeRelay)
}

// Decode parses a wire.Frame produced by Encode. It is tolerant of
// truncation / trailing bytes and never panics.
func Decode(frame []byte) (*Envelope, error) {
	typ, payload, err := wire.DecodeFrame(frame)
	if err != nil {
		return nil, err
	}
	if typ != frameTypeRelay {
		return nil, fmt.Errorf("%w: wrong wire type %d", ErrEnvelope, typ)
	}
	return DecodeBody(payload)
}

// DecodeBody parses just the envelope body (i.e. the wire-frame payload).
// pkg/dht hands us this directly via Config.Extension, so the outer frame is
// already consumed. Reads the fixed-size fields directly into the result
// struct via ReadFixedInto so we do not pay for three intermediate slices.
func DecodeBody(payload []byte) (*Envelope, error) {
	b := wire.NewBuffer(payload)
	ver, err := b.ReadUint8()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	if ver != envelopeVersion {
		return nil, fmt.Errorf("%w: version=%d", ErrEnvelope, ver)
	}
	env := &Envelope{}
	if err := b.ReadFixedInto(env.Recipient[:]); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	if err := b.ReadFixedInto(env.Sender[:]); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	if err := b.ReadFixedInto(env.SessionID[:]); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	hops, err := b.ReadUint8()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	env.Hops = hops
	innerType, err := b.ReadUint8()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	env.InnerType = innerType
	inner, err := b.ReadBytes(MaxInnerSize)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	env.Payload = inner
	if err := b.SkipUnknownTLVs(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	return env, nil
}
