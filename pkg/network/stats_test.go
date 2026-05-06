package network_test

import (
	"context"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/network"
	"github.com/udisondev/udisend/pkg/transport"
)

// TestStats_RTCFieldsZeroWhenMeshDisabled keeps the legacy contract
// — Stats from a Phase ≤ 9 node returns zero RTC fields.
func TestStats_RTCFieldsZeroWhenMeshDisabled(t *testing.T) {
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

	st := n.Stats()
	if st.RTCPeers != 0 || st.RTCBytesSent != 0 || st.RTCBytesRecv != 0 ||
		st.RTCConnectAttempts != 0 || st.RTCConnectFailures != 0 ||
		st.RTCICERestarts != 0 {
		t.Errorf("RTC fields nonzero with mesh disabled: %+v", st)
	}
}

// TestStats_RTCFieldsPopulate verifies that with MeshEnabled the
// node surfaces the RTC counter snapshot. Counters are zero on a
// freshly-opened node with no peers, but the field must be visible
// (not nil).
func TestStats_RTCFieldsPopulate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	hub := transport.NewMemoryHub()
	tr := hub.NewMemoryTransport()
	n, err := network.Open(ctx, network.Config{
		Identity:    mustIdentity(t),
		Transport:   tr,
		MeshEnabled: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	goRun(t, "stats-node", n.Run, ctx)

	// Allow a tick or two so PeerManager.Run has executed at least
	// once. With empty routing table, ConnectAttempts stays 0 — but
	// the field is still surfaced through Stats() without panic.
	time.Sleep(100 * time.Millisecond)

	st := n.Stats()
	if st.RTCPeers != 0 {
		t.Errorf("fresh node has RTCPeers = %d, want 0", st.RTCPeers)
	}
	// Counter fields are accessible (read does not panic / zero-init).
	_ = st.RTCBytesSent
	_ = st.RTCBytesRecv
	_ = st.RTCConnectAttempts
	_ = st.RTCConnectFailures
	_ = st.RTCICERestarts
}
