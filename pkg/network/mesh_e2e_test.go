package network_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/network"
	"github.com/udisondev/udisend/pkg/transport"
)

// twoMeshNodes spins up A and B with MeshEnabled and waits for both
// sides to resolve each other's presence record. Returns only after
// the routing tables and presence caches are warm enough that
// PeerManager.SelectPeers will see the other side as a viable
// candidate.
func twoMeshNodes(t *testing.T) (*network.Node, *network.Node) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	hub := transport.NewMemoryHub()
	trA := hub.NewMemoryTransport()
	trB := hub.NewMemoryTransport()

	a, err := network.Open(ctx, network.Config{
		Identity:     mustIdentity(t),
		Transport:    trA,
		MeshEnabled:  true,
		MaxMeshLinks: 2,
	})
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("close A: %v", err)
		}
	})
	goRun(t, "A", a.Run, ctx)

	b, err := network.Open(ctx, network.Config{
		Identity:     mustIdentity(t),
		Transport:    trB,
		Bootstrap:    []string{a.LocalAddress()},
		MeshEnabled:  true,
		MaxMeshLinks: 2,
	})
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("close B: %v", err)
		}
	})
	goRun(t, "B", b.Run, ctx)

	// Wait for mutual presence resolution — same gate as twoNodes.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		lctx, lcancel := context.WithTimeout(ctx, 250*time.Millisecond)
		_, errA := a.Lookup(lctx, b.Identity().Public().DestinationHash())
		lcancel()
		lctx, lcancel = context.WithTimeout(ctx, 250*time.Millisecond)
		_, errB := b.Lookup(lctx, a.Identity().Public().DestinationHash())
		lcancel()
		if errA == nil && errB == nil {
			return a, b
		}
		if errors.Is(errA, context.Canceled) || errors.Is(errB, context.Canceled) {
			t.Fatal("ctx canceled during convergence")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("nodes never converged on each other's presence")

	return nil, nil
}

// TestMesh_E2E_TwoNodesEstablishLink drives a full Phase 10 flow:
// two real network.Nodes converge on each other's presence, their
// PeerManagers see the peer as mesh-capable (CapCanWebRTCMesh
// advertised when MeshEnabled), call WebRTCTransport.Connect which
// drives a real pion handshake through the signaling.Channel
// (Noise XK end-to-end) — and the resulting DataChannel survives.
//
// This is the smallest meaningful resilience proof in Phase 10:
// without the e2e link working, the bigger "kill 2 publics" tests
// have no foundation to stand on.
func TestMesh_E2E_TwoNodesEstablishLink(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("requires pion ICE — skipped under -short")
	}

	a, b := twoMeshNodes(t)

	aHash := a.Identity().Public().DestinationHash()
	bHash := b.Identity().Public().DestinationHash()

	// PeerManager fires reconcile every 30s by default but the
	// initial reconcile happens immediately on Run. Wait up to 20s
	// for both sides to see the live mesh connection.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if a.MeshTransport().IsConnected(bHash) && b.MeshTransport().IsConnected(aHash) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !a.MeshTransport().IsConnected(bHash) {
		t.Errorf("A never opened a mesh DataChannel to B")
	}
	if !b.MeshTransport().IsConnected(aHash) {
		t.Errorf("B never opened a mesh DataChannel to A")
	}

	statsA := a.Stats()
	statsB := b.Stats()
	if statsA.RTCPeers != 1 {
		t.Errorf("A.Stats RTCPeers = %d, want 1", statsA.RTCPeers)
	}
	if statsB.RTCPeers != 1 {
		t.Errorf("B.Stats RTCPeers = %d, want 1", statsB.RTCPeers)
	}
	// Exactly one side initiated; the other is a responder. Sum of
	// attempts is at least 1.
	if statsA.RTCConnectAttempts+statsB.RTCConnectAttempts < 1 {
		t.Errorf("Sum RTCConnectAttempts = %d, want ≥ 1",
			statsA.RTCConnectAttempts+statsB.RTCConnectAttempts)
	}
}
