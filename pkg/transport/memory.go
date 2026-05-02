package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// MemoryHub wires multiple MemoryTransport instances together for use in
// tests. It implements an in-process packet bus: a transport sends to a
// destination MemoryAddr, the hub looks up the matching transport and
// delivers the packet.
type MemoryHub struct {
	mu      sync.RWMutex
	peers   map[string]*MemoryTransport
	counter atomic.Uint64
}

// NewMemoryHub returns an empty hub.
func NewMemoryHub() *MemoryHub {
	return &MemoryHub{peers: make(map[string]*MemoryTransport)}
}

// MemoryAddr is the in-process address scheme: "mem:<id>".
type MemoryAddr struct{ id string }

// Network reports the network type.
func (a MemoryAddr) Network() string { return "mem" }

// String returns the canonical "mem:id" form.
func (a MemoryAddr) String() string { return "mem:" + a.id }

// ID returns the bare id portion (without the "mem:" scheme prefix).
// Tests that need to swap registrations on a hub use this to look up
// the existing entry by its caller-chosen / counter-derived id.
func (a MemoryAddr) ID() string { return a.id }

// MemoryTransport is a Transport backed by a MemoryHub.
//
// `in` is the multi-producer fan-in chan (any peer transport sends to it
// via Send). It is **never closed** — multiple writers cannot all be made
// to stop sending atomically with a close, so closing would race with
// in-flight Sends and panic. Instead, Close signals via `closed` and the
// forwarder drains `in` then closes `out` so external Inbox consumers get
// the standard "channel closed" exit.
type MemoryTransport struct {
	hub  *MemoryHub
	addr MemoryAddr
	in   chan Packet
	out  chan Packet

	closeOnce sync.Once
	closed    chan struct{}
}

// NewMemoryTransport registers a fresh transport with the hub. Each call
// produces a unique address.
func (h *MemoryHub) NewMemoryTransport() *MemoryTransport {
	id := strconv.FormatUint(h.counter.Add(1), 10)
	return h.NewNamedMemoryTransport(id)
}

// NewNamedMemoryTransport registers a transport with a caller-chosen id.
// Useful when tests want predictable addresses.
func (h *MemoryHub) NewNamedMemoryTransport(id string) *MemoryTransport {
	t := &MemoryTransport{
		hub:    h,
		addr:   MemoryAddr{id: id},
		in:     make(chan Packet, 256),
		out:    make(chan Packet, 256),
		closed: make(chan struct{}),
	}
	go t.forward()

	h.mu.Lock()
	h.peers[id] = t
	h.mu.Unlock()

	return t
}

// Swap atomically replaces the hub registration for `id` and returns
// the previously-registered transport (nil if none). Tests use it to
// install a man-in-the-middle in front of an existing peer and then
// restore it. Production code should not call this.
func (h *MemoryHub) Swap(id string, t *MemoryTransport) *MemoryTransport {
	h.mu.Lock()
	prev := h.peers[id]
	if t == nil {
		delete(h.peers, id)
	} else {
		h.peers[id] = t
	}
	h.mu.Unlock()

	return prev
}

// forward proxies in→out and is the single closer of out, so external
// Inbox consumers see a normal channel-close on shutdown.
func (t *MemoryTransport) forward() {
	defer close(t.out)
	for {
		select {
		case <-t.closed:
			return
		case pkt := <-t.in:
			select {
			case t.out <- pkt:
			case <-t.closed:
				return
			}
		}
	}
}

// LocalAddr returns the transport's MemoryAddr.
func (t *MemoryTransport) LocalAddr() net.Addr { return t.addr }

// Dial parses a "mem:<id>" address.
func (t *MemoryTransport) Dial(addr string) (net.Addr, error) {
	return ParseMemoryAddr(addr)
}

// Send delivers payload to the transport bound to `to`. Returns an error if
// the destination is unknown or closed.
func (t *MemoryTransport) Send(ctx context.Context, to net.Addr, payload []byte) error {
	select {
	case <-t.closed:
		return ErrClosed
	default:
	}
	if len(payload) > MaxPacketSize {
		return fmt.Errorf("transport: payload %dB exceeds max %d", len(payload), MaxPacketSize)
	}
	dst, ok := to.(MemoryAddr)
	if !ok {
		return fmt.Errorf("transport: memory transport got non-memory addr %T", to)
	}
	t.hub.mu.RLock()
	target, ok := t.hub.peers[dst.id]
	t.hub.mu.RUnlock()
	if !ok {
		return fmt.Errorf("transport: unknown peer %q", dst.id)
	}
	pkt := Packet{
		From:    t.addr,
		Payload: append([]byte(nil), payload...),
	}
	select {
	case target.in <- pkt:
		return nil
	case <-target.closed:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Inbox delivers received packets.
func (t *MemoryTransport) Inbox() <-chan Packet { return t.out }

// Close removes the transport from the hub and signals the forwarder.
// `t.in` is intentionally NOT closed: it has multiple producers (other
// transports' Send calls), and a receiver-side close would race with
// in-flight sends and panic. Senders detect shutdown via `t.closed`.
func (t *MemoryTransport) Close() error {
	t.closeOnce.Do(func() {
		t.hub.mu.Lock()
		delete(t.hub.peers, t.addr.id)
		t.hub.mu.Unlock()
		close(t.closed)
	})
	return nil
}

// Compile-time assertion that MemoryTransport satisfies Transport.
var _ Transport = (*MemoryTransport)(nil)

// Compile-time assertion that UDPTransport satisfies Transport.
var _ Transport = (*UDPTransport)(nil)

// ParseMemoryAddr parses "mem:<id>" into a MemoryAddr.
func ParseMemoryAddr(s string) (MemoryAddr, error) {
	id, ok := strings.CutPrefix(s, "mem:")
	if !ok || id == "" {
		return MemoryAddr{}, errors.New("transport: not a mem address")
	}

	return MemoryAddr{id: id}, nil
}
