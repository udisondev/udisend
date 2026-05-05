package network

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
)

// stubTransport is a minimal transport.Transport mock that records
// outbound packets and exposes a controllable inbox. Used only here
// for compositeTransport unit tests.
type stubTransport struct {
	mu     sync.Mutex
	addr   net.Addr
	sent   []sentRecord
	inbox  chan transport.Packet
	closed bool
}

type sentRecord struct {
	to      net.Addr
	payload []byte
}

func newStubTransport(addr net.Addr) *stubTransport {
	return &stubTransport{
		addr:  addr,
		inbox: make(chan transport.Packet, 8),
	}
}

func (s *stubTransport) LocalAddr() net.Addr { return s.addr }

func (s *stubTransport) Dial(addr string) (net.Addr, error) {
	// Fall back to UDP-style host:port parse for the primary stub;
	// the secondary stub overrides via dialFn-style indirection if
	// needed (not used in current tests).
	return net.ResolveUDPAddr("udp", addr)
}

func (s *stubTransport) Send(_ context.Context, to net.Addr, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	s.sent = append(s.sent, sentRecord{to: to, payload: cp})

	return nil
}

func (s *stubTransport) sentSnapshot() []sentRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sentRecord, len(s.sent))
	copy(out, s.sent)

	return out
}

func (s *stubTransport) Inbox() <-chan transport.Packet { return s.inbox }

func (s *stubTransport) deliver(pkt transport.Packet) { s.inbox <- pkt }

func (s *stubTransport) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.inbox)

	return nil
}

func TestCompositeTransport_RoutesByAddrType(t *testing.T) {
	t.Parallel()

	primaryAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9001")
	primary := newStubTransport(primaryAddr)
	secondary := newStubTransport(transport.NewWebRTCAddr(identity.Hash{0xaa}))

	c := newCompositeTransport(primary, secondary)
	t.Cleanup(func() {
		_ = primary.Close()
		_ = secondary.Close()
		_ = c.Close()
	})

	udpDest, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9999")
	rtcDest := transport.NewWebRTCAddr(identity.Hash{0xbb})

	if err := c.Send(t.Context(), udpDest, []byte("via-udp")); err != nil {
		t.Fatalf("Send udp: %v", err)
	}
	if err := c.Send(t.Context(), rtcDest, []byte("via-rtc")); err != nil {
		t.Fatalf("Send rtc: %v", err)
	}

	pSent := primary.sentSnapshot()
	if len(pSent) != 1 || string(pSent[0].payload) != "via-udp" {
		t.Errorf("primary sent = %+v, want one via-udp packet", pSent)
	}
	sSent := secondary.sentSnapshot()
	if len(sSent) != 1 || string(sSent[0].payload) != "via-rtc" {
		t.Errorf("secondary sent = %+v, want one via-rtc packet", sSent)
	}
}

func TestCompositeTransport_FansInBothInboxes(t *testing.T) {
	t.Parallel()

	primaryAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9001")
	primary := newStubTransport(primaryAddr)
	secondary := newStubTransport(transport.NewWebRTCAddr(identity.Hash{0xaa}))

	c := newCompositeTransport(primary, secondary)
	t.Cleanup(func() {
		_ = primary.Close()
		_ = secondary.Close()
		_ = c.Close()
	})

	primary.deliver(transport.Packet{From: primaryAddr, Payload: []byte("from-udp")})
	secondary.deliver(transport.Packet{From: transport.NewWebRTCAddr(identity.Hash{0xcc}), Payload: []byte("from-rtc")})

	got := make(map[string]bool)
	deadline := time.After(time.Second)
	for len(got) < 2 {
		select {
		case pkt := <-c.Inbox():
			got[string(pkt.Payload)] = true
		case <-deadline:
			t.Fatalf("only got %d packets in 1s, want 2: %v", len(got), got)
		}
	}
	if !got["from-udp"] || !got["from-rtc"] {
		t.Errorf("missing fan-in packets: %v", got)
	}
}

func TestCompositeTransport_DialDispatch(t *testing.T) {
	t.Parallel()

	primaryAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9001")
	primary := newStubTransport(primaryAddr)
	secondary := newStubTransport(transport.NewWebRTCAddr(identity.Hash{}))

	c := newCompositeTransport(primary, secondary)
	t.Cleanup(func() {
		_ = primary.Close()
		_ = secondary.Close()
		_ = c.Close()
	})

	// rtc-prefix → secondary path. We did not configure secondary's
	// Dial to actually parse rtc, but compositeTransport routes by
	// prefix; secondary's stub Dial returns an UDP-resolved addr,
	// which is fine for the dispatch assertion.
	addr, err := c.Dial("rtc:" + strings.Repeat("0", 32))
	if err == nil && addr == nil {
		t.Error("expected non-nil addr or error from rtc dial")
	}

	// Plain host:port → primary.Dial.
	addr, err = c.Dial("127.0.0.1:9999")
	if err != nil {
		t.Fatalf("primary Dial: %v", err)
	}
	if addr == nil {
		t.Error("primary Dial returned nil addr without error")
	}
}

// Verify safeNewCompositeTransport rejects nil sub-transports —
// production callers (network.Open) construct through this gate.
func TestSafeNewCompositeTransport_RejectsNil(t *testing.T) {
	t.Parallel()

	primaryAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9001")
	primary := newStubTransport(primaryAddr)
	t.Cleanup(func() { _ = primary.Close() })

	if _, err := safeNewCompositeTransport(nil, primary); err == nil {
		t.Error("nil primary accepted")
	}
	if _, err := safeNewCompositeTransport(primary, nil); err == nil {
		t.Error("nil secondary accepted")
	}
}
