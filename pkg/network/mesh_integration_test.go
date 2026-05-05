package network_test

import (
	"context"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/network"
	"github.com/udisondev/udisend/pkg/transport"
)

// TestNode_MeshEnabled_StartsAndStops verifies that Open with
// MeshEnabled wires up the mesh stack (MeshSignaler, WebRTCTransport,
// PeerManager) and Run/Close cleanly tears them down without leaking
// goroutines or panicking on empty routing tables.
//
// We do NOT exercise actual pion-backed mesh handshake here — that
// belongs in the e2e suite (Phase 10.10). This test is the wiring
// smoke check: with no peers in the routing table, the manager's
// reconcile passes return empty and the node still shuts down
// cleanly.
func TestNode_MeshEnabled_StartsAndStops(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	hub := transport.NewMemoryHub()
	tr := hub.NewMemoryTransport()

	n, err := network.Open(ctx, network.Config{
		Identity:     mustIdentity(t),
		Transport:    tr,
		MeshEnabled:  true,
		MaxMeshLinks: 4,
	})
	if err != nil {
		t.Fatalf("Open with MeshEnabled: %v", err)
	}
	t.Cleanup(func() {
		if err := n.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	if n.MeshTransport() == nil {
		t.Error("MeshTransport() is nil despite MeshEnabled=true")
	}
	if n.MeshPeerManager() == nil {
		t.Error("MeshPeerManager() is nil despite MeshEnabled=true")
	}

	goRun(t, "mesh-node", n.Run, ctx)

	// Allow the manager's first reconcile to fire; with empty
	// routing table it returns no candidates and PeerManager idles.
	time.Sleep(100 * time.Millisecond)
}

// TestNode_MeshDisabled_DefaultsToNil sanity-checks the legacy /
// Phase ≤ 9 path — Open without MeshEnabled returns a Node whose
// mesh accessors yield nil.
func TestNode_MeshDisabled_DefaultsToNil(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	hub := transport.NewMemoryHub()
	tr := hub.NewMemoryTransport()

	n, err := network.Open(ctx, network.Config{
		Identity:  mustIdentity(t),
		Transport: tr,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	if n.MeshTransport() != nil {
		t.Error("MeshTransport() != nil with MeshEnabled=false")
	}
	if n.MeshPeerManager() != nil {
		t.Error("MeshPeerManager() != nil with MeshEnabled=false")
	}
}
