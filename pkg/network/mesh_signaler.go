package network

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// meshSignalerInboxSize bounds the buffered backlog of inbound mesh
// events held for the WebRTCTransport reader. 128 is generous: a peer
// reconnect storm produces ≤ 3 messages per peer (offer / answer /
// candidate batch) and PeerManager processes events well under a
// millisecond each. If we ever drop on a full channel, that is a sign
// of a stuck consumer rather than a tuning problem.
const meshSignalerInboxSize = 128

// MeshSignaler bridges signaling.Service to transport.Signaler. It
// manages a per-peer Channel pool: outbound SendMeshSDP calls open or
// reuse a Channel via Service.Connect; inbound mesh envelopes are
// captured through Service.SetMeshHandler and surfaced on
// RecvMeshSDP. Each Channel here is dedicated to mesh handshake — it
// is opened separately from any application-traffic Channel between
// the same identities.
type MeshSignaler struct {
	svc      *signaling.Service
	logger   *slog.Logger
	incoming chan transport.MeshSDPMsg

	mu       sync.Mutex
	channels map[identity.Hash]*signaling.Channel
}

// NewMeshSignaler constructs a bridge and registers itself as the
// mesh-handler on svc. Calling NewMeshSignaler twice on the same
// Service overwrites the prior handler (the bridge does not multiplex
// — each Service has at most one mesh path at a time).
func NewMeshSignaler(svc *signaling.Service, logger *slog.Logger) *MeshSignaler {
	if logger == nil {
		logger = slog.Default()
	}
	ms := &MeshSignaler{
		svc:      svc,
		logger:   logger,
		incoming: make(chan transport.MeshSDPMsg, meshSignalerInboxSize),
		channels: make(map[identity.Hash]*signaling.Channel),
	}
	svc.SetMeshHandler(ms.handle)

	return ms
}

// SendMeshSDP encrypts and ships sdp to peer. If a mesh channel is
// not yet open, this calls signaling.Service.Connect to bring one up
// (which performs the Noise XK handshake before returning). On
// success the Channel is cached for subsequent SendMeshSDP calls.
func (ms *MeshSignaler) SendMeshSDP(
	ctx context.Context,
	peer identity.PeerID,
	kind transport.MeshSDPKind,
	sdp []byte,
) error {
	inner, err := innerForKind(kind)
	if err != nil {
		return err
	}

	ch, err := ms.getOrOpen(ctx, peer.Bytes())
	if err != nil {
		return fmt.Errorf("network: mesh channel: %w", err)
	}

	return ch.SendMesh(ctx, inner, sdp)
}

// RecvMeshSDP returns the inbound mesh event stream. The channel is
// never closed by MeshSignaler — consumers gate on their own context.
func (ms *MeshSignaler) RecvMeshSDP() <-chan transport.MeshSDPMsg { return ms.incoming }

// Close releases the mesh handler so the underlying signaling.Service
// no longer fans events into this bridge. In-flight Channels remain
// open — they are owned by the Service and will be torn down on its
// Close.
func (ms *MeshSignaler) Close() {
	ms.svc.SetMeshHandler(nil)
}

// handle is the signaling.Service mesh-handler. It records the
// channel under the peer key (so the responder side can SendMeshSDP
// back without opening a duplicate Channel) and forwards the event.
func (ms *MeshSignaler) handle(ch *signaling.Channel, inner byte, sdp []byte) {
	kind, err := kindForInner(inner)
	if err != nil {
		ms.logger.Debug("mesh-signaler: drop unknown inner", "type", inner)
		return
	}
	peer := ch.Peer()

	ms.mu.Lock()
	if existing, ok := ms.channels[peer]; ok && existing != ch {
		// Two channels concurrent for one peer — keep the live one
		// (the mesh-handler delivered through it). The other will be
		// reaped when its noise session times out.
	}
	ms.channels[peer] = ch
	ms.mu.Unlock()

	// Copy the slice: signaling.Channel.handleMesh allocates fresh
	// plaintext via noise.Decrypt, so it is safe to share — but the
	// handler runs on the dispatcher goroutine, and ms.incoming may
	// outlive the dispatcher's reference. Avoid the lifetime debate
	// by depositing a defensive copy.
	cp := append([]byte(nil), sdp...)

	select {
	case ms.incoming <- transport.MeshSDPMsg{Peer: peer, Kind: kind, SDP: cp}:
	default:
		ms.logger.Warn("mesh-signaler: incoming queue full; dropping",
			"peer", peer, "kind", kind)
	}
}

// getOrOpen returns the cached Channel for peer if any, otherwise
// opens a fresh one via Service.Connect and atomically installs it.
// Concurrent callers race on Connect — the loser closes its Channel
// and returns the winner's. This avoids dual open (two outbound
// HELLO_INIT envelopes for the same peer-pair) without serialising
// every SendMeshSDP through one mutex.
func (ms *MeshSignaler) getOrOpen(ctx context.Context, peer identity.Hash) (*signaling.Channel, error) {
	ms.mu.Lock()
	if ch, ok := ms.channels[peer]; ok {
		ms.mu.Unlock()
		return ch, nil
	}
	ms.mu.Unlock()

	ch, err := ms.svc.Connect(ctx, peer)
	if err != nil {
		return nil, err
	}

	ms.mu.Lock()
	if existing, ok := ms.channels[peer]; ok && existing != ch {
		ms.mu.Unlock()
		// Lost the race; close the one we just opened.
		_ = ch.Close()
		return existing, nil
	}
	ms.channels[peer] = ch
	ms.mu.Unlock()

	return ch, nil
}

func innerForKind(kind transport.MeshSDPKind) (byte, error) {
	switch kind {
	case transport.MeshSDPOffer:
		return signaling.InnerMeshOffer, nil
	case transport.MeshSDPAnswer:
		return signaling.InnerMeshAnswer, nil
	case transport.MeshSDPCandidate:
		return signaling.InnerMeshCandidate, nil
	default:
		return 0, fmt.Errorf("network: unknown MeshSDPKind %d", kind)
	}
}

func kindForInner(inner byte) (transport.MeshSDPKind, error) {
	switch inner {
	case signaling.InnerMeshOffer:
		return transport.MeshSDPOffer, nil
	case signaling.InnerMeshAnswer:
		return transport.MeshSDPAnswer, nil
	case signaling.InnerMeshCandidate:
		return transport.MeshSDPCandidate, nil
	default:
		return 0, fmt.Errorf("network: unknown inner kind %d", inner)
	}
}

// Compile-time assertion that MeshSignaler satisfies the Signaler
// contract WebRTCTransport consumes.
var _ transport.Signaler = (*MeshSignaler)(nil)
