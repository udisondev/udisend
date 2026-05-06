package webrtc_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
	udwebrtc "github.com/udisondev/udisend/pkg/webrtc"
)

// TestPeerSession_OfferAnswerLoopback exercises the full pion-backed
// path: A (initiator) creates offer → B (responder) answers → ICE
// candidates trickle both ways → DataChannel opens → 1 KiB payload
// flows A→B and B→A. No external STUN/TURN — host candidates over
// loopback are sufficient.
func TestPeerSession_OfferAnswerLoopback(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("requires pion ICE — skipped under -short")
	}

	var aPeer, bPeer identity.Hash
	aPeer[0] = 0xaa
	bPeer[0] = 0xbb

	// atomic.Pointer trick: peers reference each other for piping,
	// but they are constructed in sequence. Pre-allocate the pointers
	// then resolve once both exist.
	var aPtr, bPtr atomic.Pointer[udwebrtc.PeerSession]

	pipeSDP := func(target *atomic.Pointer[udwebrtc.PeerSession]) func(udwebrtc.SDPKind, []byte) {
		return func(kind udwebrtc.SDPKind, sdp []byte) {
			peer := target.Load()
			if peer == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			switch kind {
			case udwebrtc.SDPOffer:
				if err := peer.AcceptOffer(ctx, sdp); err != nil {
					t.Errorf("pipe AcceptOffer: %v", err)
				}
			case udwebrtc.SDPAnswer:
				if err := peer.AcceptAnswer(ctx, sdp); err != nil {
					t.Errorf("pipe AcceptAnswer: %v", err)
				}
			}
		}
	}
	pipeICE := func(target *atomic.Pointer[udwebrtc.PeerSession]) func(string) {
		return func(cand string) {
			peer := target.Load()
			if peer == nil {
				return
			}
			if err := peer.AddRemoteCandidate(cand); err != nil {
				// Late candidates after Close are not interesting.
				return
			}
		}
	}

	a, err := udwebrtc.NewPeerSession(udwebrtc.PeerSessionConfig{
		Peer:  bPeer,
		OnSDP: pipeSDP(&bPtr),
		OnICE: pipeICE(&bPtr),
	})
	if err != nil {
		t.Fatalf("new A: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	aPtr.Store(a)

	b, err := udwebrtc.NewPeerSession(udwebrtc.PeerSessionConfig{
		Peer:  aPeer,
		OnSDP: pipeSDP(&aPtr),
		OnICE: pipeICE(&aPtr),
	})
	if err != nil {
		t.Fatalf("new B: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	bPtr.Store(b)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if err := a.CreateOffer(ctx); err != nil {
		t.Fatalf("a.CreateOffer: %v", err)
	}

	// Wait for the data channel to open on both ends — A holds the
	// initiator-side DC handle, B picks it up via OnDataChannel.
	openA, openB := a.Opened(), b.Opened()
	select {
	case <-openA:
	case <-time.After(8 * time.Second):
		t.Fatal("A's DataChannel never opened")
	}
	select {
	case <-openB:
	case <-time.After(8 * time.Second):
		t.Fatal("B's DataChannel never opened")
	}

	// 1 KiB round-trip A → B.
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	if err := a.Send(payload); err != nil {
		t.Fatalf("a.Send: %v", err)
	}
	select {
	case got := <-b.Recv():
		if len(got) != len(payload) {
			t.Errorf("B got %d bytes, want %d", len(got), len(payload))
		}
		for i := range got {
			if got[i] != payload[i] {
				t.Errorf("byte %d: got %#x want %#x", i, got[i], payload[i])
				break
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("B never received payload")
	}

	// Reverse direction: B → A.
	reply := []byte("ack-from-B")
	if err := b.Send(reply); err != nil {
		t.Fatalf("b.Send: %v", err)
	}
	select {
	case got := <-a.Recv():
		if string(got) != string(reply) {
			t.Errorf("A got %q, want %q", got, reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("A never received reply")
	}
}

// TestPeerSession_PeerHashStored sanity-checks that the constructor
// records the destination hash for use by upstream PeerManager.
func TestPeerSession_PeerHashStored(t *testing.T) {
	t.Parallel()

	var h identity.Hash
	h[0] = 0x42

	p, err := udwebrtc.NewPeerSession(udwebrtc.PeerSessionConfig{Peer: h})
	if err != nil {
		t.Fatalf("NewPeerSession: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	if p.Peer() != h {
		t.Errorf("Peer() = %x, want %x", p.Peer(), h)
	}
}

// TestPeerSession_RejectsLargePayload ensures Send refuses payloads
// past the SCTP message-size cap, so callers get a clear error rather
// than an opaque pion-side failure.
func TestPeerSession_RejectsLargePayload(t *testing.T) {
	t.Parallel()

	var h identity.Hash
	p, err := udwebrtc.NewPeerSession(udwebrtc.PeerSessionConfig{Peer: h})
	if err != nil {
		t.Fatalf("NewPeerSession: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	// Even before DC opens, payload-size validation is purely-functional.
	huge := make([]byte, 64*1024+1)
	if err := p.Send(huge); err == nil {
		t.Error("Send(huge) = nil, want size error")
	}
}

// Compile-time: ensures Signaler kind enum is referenced so tests in
// this package can route between PeerSession and the wider transport.
var _ transport.MeshSDPKind

// TestPeerSession_RangeRecvExitsAfterClose verifies that consumers
// using `for msg := range sess.Recv()` exit cleanly when the session
// is closed. Iter-3 review caught that `inbox` was never closed —
// readers using the range form would hang indefinitely. The fix
// drains in-flight OnMessage callbacks via recvWg before closing
// inbox.
func TestPeerSession_RangeRecvExitsAfterClose(t *testing.T) {
	t.Parallel()

	var h identity.Hash
	p, err := udwebrtc.NewPeerSession(udwebrtc.PeerSessionConfig{Peer: h})
	if err != nil {
		t.Fatalf("NewPeerSession: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range p.Recv() {
			// drain
		}
	}()

	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("range over Recv() did not exit after Close")
	}
}
