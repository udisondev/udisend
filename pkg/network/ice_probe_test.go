package network

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/stun"
)

// TestProbeSTUNReachable_AcceptsRealResponder boots a real STUN server
// on a loopback port and confirms the probe accepts it.
func TestProbeSTUNReachable_AcceptsRealResponder(t *testing.T) {
	t.Parallel()

	srv, err := stun.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = srv.Run(ctx) }()

	pctx, pcancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer pcancel()
	if err := probeSTUNReachable(pctx, srv.LocalAddr().String()); err != nil {
		t.Fatalf("probe of real STUN server failed: %v", err)
	}
}

// TestProbeSTUNReachable_RejectsSilentTarget is the capability-spoofing
// regression: a presence record claiming `CapCanSTUN` and pointing at
// an IP that does not actually run STUN must NOT survive the probe.
// We bind a UDP socket without serving STUN, so the kernel either
// discards or "ICMP port unreachable"s the request and we time out —
// either way the probe returns an error.
func TestProbeSTUNReachable_RejectsSilentTarget(t *testing.T) {
	t.Parallel()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	pctx, pcancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer pcancel()
	if err := probeSTUNReachable(pctx, conn.LocalAddr().String()); err == nil {
		t.Errorf("probe accepted silent UDP socket; capability spoofing not blocked")
	}
}

// TestProbeSTUNReachable_RejectsClosedPort confirms that a port with
// no listener at all is rejected. With Linux returning ICMP port
// unreachable, conn.Read returns the error before the deadline; on
// other platforms we fall back to the deadline. Either path returns
// non-nil from the probe.
func TestProbeSTUNReachable_RejectsClosedPort(t *testing.T) {
	t.Parallel()

	// Pick an unused port by binding then closing.
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	_ = conn.Close()

	pctx, pcancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer pcancel()
	if err := probeSTUNReachable(pctx, addr); err == nil {
		t.Errorf("probe accepted closed port; capability spoofing not blocked")
	}
}
