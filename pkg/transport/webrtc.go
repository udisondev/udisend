package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"strings"

	"github.com/udisondev/udisend/pkg/identity"
)

// WebRTC transport sentinel errors.
//
// ErrNoRoute is returned by Send when the destination peer has no
// established DataChannel — strict semantics, no on-demand setup
// in the hot path. Callers (typically the network Router) should
// fall back to a different transport (UDP) or trigger an explicit
// Connect via the PeerManager.
//
// ErrAlreadyConnected is returned by Connect when a session for the
// given peer already exists. The error is not fatal; callers may
// treat it as a no-op.
//
// ErrBufferFull is returned by Send when the underlying SCTP
// DataChannel's bufferedAmount has crossed the high-water mark. The
// caller should drop or queue the frame; we never silently lose data.
//
// ErrInvalidAddr is returned when an address fails to parse as the
// WebRTC scheme.
//
// ErrNotImplemented is a placeholder used by stubs during Phase 10
// build-out. It MUST not survive the phase — every method that ships
// the phase replaces this with a concrete implementation.
var (
	ErrNoRoute         = errors.New("transport: no rtc route to peer")
	ErrAlreadyConnected = errors.New("transport: rtc peer already connected")
	ErrBufferFull      = errors.New("transport: rtc data channel buffer full")
	ErrInvalidAddr     = errors.New("transport: invalid rtc address")
	ErrNotImplemented  = errors.New("transport: not implemented")
)

// rtcScheme is the network scheme of a WebRTCAddr. It mirrors the role
// of "tcp" / "udp" / "mem" in their respective transports — kept short
// because it goes into every Packet.From.Network() call site.
const rtcScheme = "rtc"

// MeshSDPKind discriminates the three mesh-handshake message types
// exchanged between two network nodes to bring up a WebRTC
// DataChannel. Values mirror the InnerMesh* constants in pkg/signaling
// at the wire layer; we keep a transport-package-local enum so the
// pkg/transport API does not depend on pkg/signaling.
type MeshSDPKind byte

// MeshSDP* identify the three message kinds.
const (
	MeshSDPOffer     MeshSDPKind = 1
	MeshSDPAnswer    MeshSDPKind = 2
	MeshSDPCandidate MeshSDPKind = 3
)

// String renders the kind as a readable label for logs / errors.
func (k MeshSDPKind) String() string {
	switch k {
	case MeshSDPOffer:
		return "offer"
	case MeshSDPAnswer:
		return "answer"
	case MeshSDPCandidate:
		return "candidate"
	default:
		return "unknown"
	}
}

// MeshSDPMsg is one inbound mesh-handshake event delivered by a
// Signaler.
type MeshSDPMsg struct {
	Peer identity.Hash
	Kind MeshSDPKind
	SDP  []byte
}

// Signaler is the port WebRTCTransport uses to exchange mesh
// SDP/ICE messages with peers via the existing signaling.Service /
// Noise XK channel infrastructure. Implementations are provided by
// pkg/network — see pkg/network.MeshSignaler. Defining the interface
// here (rather than in pkg/network) lets pkg/transport stay free of
// signaling/network imports while still expressing the contract its
// peer-management code needs.
//
// SendMeshSDP is responsible for opening or reusing a signaling
// Channel to peer, encrypting `sdp` under that Channel's Noise key,
// and shipping it as an InnerMesh{Offer,Answer,Candidate} envelope.
// It MUST return only after the wire send succeeds (or fails) — the
// caller treats the post-return state as authoritative for retry.
//
// RecvMeshSDP returns a stream of inbound mesh events. The channel
// SHOULD have a small buffer so a slow consumer cannot deadlock the
// signaling dispatch goroutine; implementations drop or block by
// their own policy. Closing the channel on shutdown is OPTIONAL —
// most consumers gate on context instead.
type Signaler interface {
	SendMeshSDP(ctx context.Context, peer identity.Hash, kind MeshSDPKind, sdp []byte) error
	RecvMeshSDP() <-chan MeshSDPMsg
}

// WebRTCAddr identifies a peer on the WebRTC mesh. It is a peer-keyed
// address, not a host:port — the underlying transport (pion's ICE
// agent) is responsible for actually finding the peer's network
// endpoint via signaling and STUN/TURN candidates.
//
// The textual form is "rtc:<hex16>" where hex16 is the 32-character
// hex encoding of identity.Hash (16 bytes).
type WebRTCAddr struct {
	peer identity.Hash
}

// NewWebRTCAddr returns the WebRTCAddr for the given peer hash.
func NewWebRTCAddr(h identity.Hash) WebRTCAddr {
	return WebRTCAddr{peer: h}
}

// Peer returns the destination hash this address points at.
func (a WebRTCAddr) Peer() identity.Hash { return a.peer }

// Network reports the address scheme.
func (a WebRTCAddr) Network() string { return rtcScheme }

// String returns the canonical "rtc:<hex16>" form.
func (a WebRTCAddr) String() string {
	return rtcScheme + ":" + hex.EncodeToString(a.peer[:])
}

// ParseWebRTCAddr parses a textual "rtc:<hex16>" address.
func ParseWebRTCAddr(s string) (WebRTCAddr, error) {
	rest, ok := strings.CutPrefix(s, rtcScheme+":")
	if !ok {
		return WebRTCAddr{}, ErrInvalidAddr
	}
	if len(rest) != 2*identity.HashSize {
		return WebRTCAddr{}, ErrInvalidAddr
	}
	raw, err := hex.DecodeString(rest)
	if err != nil {
		return WebRTCAddr{}, ErrInvalidAddr
	}
	var h identity.Hash
	copy(h[:], raw)

	return WebRTCAddr{peer: h}, nil
}

// WebRTCTransport implements transport.Transport over pion/webrtc
// DataChannels. Each peer is reached via its identity.Hash; the
// underlying ICE agent handles NAT traversal through volunteer STUN /
// TURN nodes. Send semantics are strict — there is no implicit
// dial-on-Send. Use Connect to bring up a DataChannel; the
// PeerManager (pkg/webrtc) drives this proactively for K mesh-links.
//
// The struct is intentionally exposed as a value-zero usable shell so
// the compile-time assertion in this file (and the one in tests) keeps
// the Transport interface contract enforced even before Phase 10.5
// fills in the wiring.
type WebRTCTransport struct {
	// Phase 10.5 fills this in: signaler, peer map, inbox channel,
	// closed flag, etc. Kept empty here so the type is constructible
	// in compile-time assertions and stub tests.
	_ struct{}
}

// LocalAddr is implemented in Phase 10.5; until then it returns a
// zero WebRTCAddr so Transport.LocalAddr is never nil.
func (t *WebRTCTransport) LocalAddr() net.Addr { return WebRTCAddr{} }

// Dial parses a textual "rtc:<hex16>" address into a WebRTCAddr. It
// is the only method finished in 10.2 because address parsing is the
// purely-functional bit; everything else awaits Phase 10.5.
func (t *WebRTCTransport) Dial(addr string) (net.Addr, error) {
	a, err := ParseWebRTCAddr(addr)
	if err != nil {
		return nil, err
	}

	// Phase 10.5 will wire Dial into the peer map; until then the
	// caller cannot do anything with the address but parse it. Return
	// the parsed value alongside ErrNotImplemented so the address is
	// available for inspection in tests.
	return a, ErrNotImplemented
}

// Send is implemented in Phase 10.5.
func (t *WebRTCTransport) Send(_ context.Context, _ net.Addr, _ []byte) error {
	return ErrNotImplemented
}

// Inbox is implemented in Phase 10.5. Returning a closed channel keeps
// any consumer that ranges over Inbox() from blocking forever — the
// range exits immediately.
func (t *WebRTCTransport) Inbox() <-chan Packet {
	ch := make(chan Packet)
	close(ch)

	return ch
}

// Close is implemented in Phase 10.5.
func (t *WebRTCTransport) Close() error { return ErrNotImplemented }

// Compile-time assertion that WebRTCTransport satisfies Transport.
var _ Transport = (*WebRTCTransport)(nil)
