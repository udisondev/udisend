package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// udpRecvPool reuses MaxPacketSize byte slices across recvLoop
// iterations. The hot path (a node serving thousands of clients at
// 100 packets/sec/source) previously allocated `make([]byte, n)` per
// inbound datagram — measurable GC pressure under sustained DoS.
// With the pool, alloc/op on the recv side drops to zero (modulo
// occasional pool growth). Consumers MUST call Packet.Release to
// return the buffer; the dispatcher in pkg/dht does this after
// handlePacket returns.
//
// Buffers are stored as `*[]byte` (not `[]byte`) so the pool tracks
// the allocation, not the slice header — see Go issue #16323.
var udpRecvPool = sync.Pool{
	New: func() any {
		buf := make([]byte, MaxPacketSize)
		return &buf
	},
}

// UDPTransport is a Transport backed by a UDP socket. Inbound packets are
// delivered through Inbox(); outbound via Send. Concurrency-safe.
type UDPTransport struct {
	conn *net.UDPConn
	in   chan Packet
	wg   sync.WaitGroup

	closeOnce sync.Once
	closed    chan struct{}
}

// ListenUDP binds to the given UDP address and starts the receive loop.
// Use ":0" to let the kernel pick a port; the chosen port is then visible
// via LocalAddr.
func ListenUDP(addr string) (*UDPTransport, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("transport: resolve %q: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("transport: listen %q: %w", addr, err)
	}
	t := &UDPTransport{
		conn:   conn,
		in:     make(chan Packet, 256),
		closed: make(chan struct{}),
	}
	t.wg.Add(1)
	go t.recvLoop()
	return t, nil
}

// LocalAddr returns the bound UDP address.
func (t *UDPTransport) LocalAddr() net.Addr { return t.conn.LocalAddr() }

// Dial resolves a "host:port" string into a *net.UDPAddr.
func (t *UDPTransport) Dial(addr string) (net.Addr, error) {
	return net.ResolveUDPAddr("udp", addr)
}

// Send writes payload to the given address.
func (t *UDPTransport) Send(ctx context.Context, to net.Addr, payload []byte) error {
	select {
	case <-t.closed:
		return ErrClosed
	default:
	}
	if len(payload) > MaxPacketSize {
		return fmt.Errorf("transport: payload %dB exceeds max %d", len(payload), MaxPacketSize)
	}
	udpAddr, err := resolveAddr(to)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := t.conn.SetWriteDeadline(deadline); err != nil {
			return fmt.Errorf("transport: set deadline: %w", err)
		}
	} else {
		_ = t.conn.SetWriteDeadline(time.Time{})
	}
	_, err = t.conn.WriteToUDP(payload, udpAddr)
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			return ErrClosed
		}
		return fmt.Errorf("transport: write: %w", err)
	}
	return nil
}

// Inbox delivers received packets. The channel is closed when the transport
// is closed.
func (t *UDPTransport) Inbox() <-chan Packet { return t.in }

// Close releases the underlying socket.
func (t *UDPTransport) Close() error {
	var closeErr error
	t.closeOnce.Do(func() {
		close(t.closed)
		closeErr = t.conn.Close()
	})
	t.wg.Wait()
	return closeErr
}

func (t *UDPTransport) recvLoop() {
	defer t.wg.Done()
	defer close(t.in)
	for {
		// Take a buffer from the pool BEFORE the read so the read fills
		// it directly — no intermediate copy. The pool guarantees
		// MaxPacketSize capacity, which the kernel-side UDP read
		// silently truncates to.
		bufp := udpRecvPool.Get().(*[]byte)
		buf := *bufp
		n, src, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			udpRecvPool.Put(bufp)
			select {
			case <-t.closed:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Transient errors (e.g. ICMP "port unreachable" on Linux for a
			// previous Send) — log at Debug and continue. Use a
			// cancellable wait so ctx-cancel does not get stuck behind a
			// 10 ms sleep.
			slog.Default().Debug("transport: udp read", "err", err)
			select {
			case <-time.After(10 * time.Millisecond):
			case <-t.closed:
				return
			}
			continue
		}
		select {
		case t.in <- Packet{From: src, Payload: buf[:n], pool: &udpRecvPool, bufp: bufp}:
		case <-t.closed:
			// Channel-closed path: return the buffer to the pool here
			// since no consumer will see the Packet.
			*bufp = (*bufp)[:cap(*bufp)]
			udpRecvPool.Put(bufp)
			return
		}
	}
}

func resolveAddr(addr net.Addr) (*net.UDPAddr, error) {
	if u, ok := addr.(*net.UDPAddr); ok {
		return u, nil
	}
	resolved, err := net.ResolveUDPAddr("udp", addr.String())
	if err != nil {
		return nil, fmt.Errorf("transport: resolve %q: %w", addr.String(), err)
	}
	return resolved, nil
}
