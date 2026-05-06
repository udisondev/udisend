package transport_test

// E2E tests for WebRTCTransport that exercise the real pion stack
// over an in-process Signaler. Kept in a separate file so the small
// 10.2 unit tests stay quick — these need ICE setup time.

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
)

// inProcessSignaler pipes MeshSDPMsg events between two registered
// WebRTCTransports in-memory, simulating what network.MeshSignaler
// does over a real signaling.Channel — but without the Noise XK
// handshake. Keeps WebRTCTransport tests focused on its own
// behaviour rather than signaling correctness.
type inProcessSignaler struct {
	hub *signalerHub
	me  identity.Hash
}

type signalerHub struct {
	mu    sync.Mutex
	peers map[identity.Hash]chan transport.MeshSDPMsg
}

func newSignalerHub() *signalerHub {
	return &signalerHub{peers: make(map[identity.Hash]chan transport.MeshSDPMsg)}
}

func (h *signalerHub) register(peer identity.Hash) *inProcessSignaler {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.peers[peer] = make(chan transport.MeshSDPMsg, 32)

	return &inProcessSignaler{hub: h, me: peer}
}

func (s *inProcessSignaler) SendMeshSDP(_ context.Context, peer identity.PeerID, kind transport.MeshSDPKind, sdp []byte) error {
	s.hub.mu.Lock()
	dst, ok := s.hub.peers[peer.Bytes()]
	s.hub.mu.Unlock()
	if !ok {
		return errors.New("inproc-signaler: unknown peer")
	}
	cp := append([]byte(nil), sdp...)
	select {
	case dst <- transport.MeshSDPMsg{Peer: s.me, Kind: kind, SDP: cp}:
		return nil
	case <-time.After(time.Second):
		return errors.New("inproc-signaler: send timeout")
	}
}

func (s *inProcessSignaler) RecvMeshSDP() <-chan transport.MeshSDPMsg {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()

	return s.hub.peers[s.me]
}

func makeWebRTCTransport(t *testing.T, hub *signalerHub) (*transport.WebRTCTransport, identity.Hash) {
	t.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	h := id.Public().DestinationHash()

	sig := hub.register(h)
	w, err := transport.NewWebRTCTransport(transport.WebRTCTransportConfig{
		Self:     h,
		Signaler: sig,
	})
	if err != nil {
		t.Fatalf("NewWebRTCTransport: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		_ = w.Run(ctx)
	}()
	t.Cleanup(func() {
		_ = w.Close()
	})

	return w, h
}

// TestWebRTCTransport_ConnectAndSend exercises the full path: A
// Connects to B over the in-process signaler, then Sends a payload;
// B's Inbox surfaces it.
func TestWebRTCTransport_ConnectAndSend(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("requires pion ICE — skipped under -short")
	}

	hub := newSignalerHub()
	a, _ := makeWebRTCTransport(t, hub)
	b, bHash := makeWebRTCTransport(t, hub)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if err := a.Connect(ctx, bHash); err != nil {
		t.Fatalf("a.Connect: %v", err)
	}

	addr := transport.NewWebRTCAddr(bHash)
	payload := []byte("hello-mesh")
	if err := a.Send(ctx, addr, payload); err != nil {
		t.Fatalf("a.Send: %v", err)
	}

	select {
	case pkt := <-b.Inbox():
		if string(pkt.Payload) != string(payload) {
			t.Errorf("payload = %q, want %q", pkt.Payload, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B did not receive payload")
	}
}

// TestWebRTCTransport_SendWithoutConnectFails enforces strict
// semantics: Send to a non-connected peer must NOT silently dial.
func TestWebRTCTransport_SendWithoutConnectFails(t *testing.T) {
	t.Parallel()

	hub := newSignalerHub()
	a, _ := makeWebRTCTransport(t, hub)

	var unknown identity.Hash
	unknown[0] = 0xee

	addr := transport.NewWebRTCAddr(unknown)
	err := a.Send(t.Context(), addr, []byte("x"))
	if err == nil {
		t.Fatal("Send to unconnected peer = nil, want ErrNoRoute")
	}
	if !errors.Is(err, transport.ErrNoRoute) {
		t.Errorf("err = %v, want ErrNoRoute", err)
	}
}

// TestWebRTCTransport_ConnectIdempotent verifies the singleflight
// behaviour: two concurrent Connect calls to the same peer share one
// underlying handshake; the second return waits for the first.
func TestWebRTCTransport_ConnectIdempotent(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("requires pion ICE — skipped under -short")
	}

	hub := newSignalerHub()
	a, _ := makeWebRTCTransport(t, hub)
	_, bHash := makeWebRTCTransport(t, hub)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = a.Connect(ctx, bHash)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Connect[%d]: %v", i, err)
		}
	}

	// Idempotent retry: should still succeed and not re-handshake.
	if err := a.Connect(ctx, bHash); err != nil {
		t.Errorf("re-Connect: %v", err)
	}
}
