package dht

import (
	"crypto/rand"
	"net"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
)

// TestMaybeProbe_BoundedByMaxInFlight verifies the cap on probesInFlight:
// once the global budget is exhausted, additional unique SrcIDs from
// fresh requests are silently shed instead of growing the map (and its
// per-entry goroutine) unboundedly. Without the cap, an attacker
// rotating SrcID across spoofed source IPs inflates state linearly
// with packet rate × RequestTimeout.
func TestMaybeProbe_BoundedByMaxInFlight(t *testing.T) {
	t.Parallel()

	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	hub := transport.NewMemoryHub()
	tr := hub.NewMemoryTransport()
	t.Cleanup(func() { _ = tr.Close() })

	n := NewNode(id, tr, nil, Config{})

	// Pre-fill probesInFlight to capacity with synthetic NodeIDs WITHOUT
	// going through maybeProbe (so we don't actually spawn N goroutines —
	// just the tracking state).
	n.probeMu.Lock()
	for i := range MaxProbesInFlight {
		var nid NodeID
		nid[0] = byte(i)
		nid[1] = byte(i >> 8)
		nid[2] = byte(i >> 16)
		n.probesInFlight[nid] = struct{}{}
	}
	n.probeMu.Unlock()

	// Fire many fresh SrcIDs at saturation. None must be admitted, the
	// map size must stay flat. The loop covers the broader attack shape
	// (rate × RequestTimeout), not the single-call edge case. Bytes
	// chosen to be DISJOINT from the pre-fill range (which writes
	// [0..2]) — fresh writes only [3..5] so a collision cannot
	// pre-populate a "fresh" id.
	addr, _ := net.ResolveUDPAddr("udp", "203.0.113.99:9000")
	const burst = 1024
	var freshIDs []NodeID
	for i := range burst {
		var nid NodeID
		nid[3] = byte(i)
		nid[4] = byte(i >> 8)
		nid[5] = 0xAB
		freshIDs = append(freshIDs, nid)
		n.maybeProbe(nid, addr)
	}

	n.probeMu.Lock()
	got := len(n.probesInFlight)
	admitted := 0
	for _, nid := range freshIDs {
		if _, ok := n.probesInFlight[nid]; ok {
			admitted++
		}
	}
	n.probeMu.Unlock()

	if got > MaxProbesInFlight {
		t.Errorf("probesInFlight grew past cap: %d, want ≤ %d", got, MaxProbesInFlight)
	}
	if admitted > 0 {
		t.Errorf("%d/%d fresh probes admitted at saturation; want 0", admitted, burst)
	}
}
