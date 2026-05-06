// Package transport defines the network abstraction used by the DHT and
// signaling layers. The interface deliberately models a connectionless,
// best-effort packet pipe — the same shape as UDP — so production code
// (UDPTransport) and tests (MemoryTransport) share one API.
//
// Packet size limit: 64 KiB minus headers; callers should keep messages
// well under this. Higher layers (signaling, file transfer) handle
// fragmentation themselves.
package transport

import (
	"context"
	"errors"
	"net"
	"sync"
)

// MaxPacketSize is the cap enforced on every Send and Receive. Anything
// larger is rejected before it touches the network.
const MaxPacketSize = 64 * 1024

// ErrClosed is returned from Send / Receive after Close has been called.
var ErrClosed = errors.New("transport: closed")

// Packet is an inbound datagram delivered via Inbox().
//
// Payload's backing array may belong to a transport-internal sync.Pool
// (see pkg/transport.UDPTransport). Consumers MUST call Release exactly
// once when they are done with the bytes — failure to do so leaks the
// pool slot; calling twice is an idempotent no-op. Implementations that
// don't pool buffers (e.g. MemoryTransport) ship `pool == nil` so the
// call costs one nil-check.
//
// The bytes returned to the pool are reused by subsequent recvLoop
// iterations, so consumers that retain a reference past Release MUST
// copy first. The DHT and signaling decoders already do
// (`wire.ReadString`/`ReadBytes` allocate fresh storage) — application
// code that keeps a slice longer than the dispatcher callback should
// either copy or refrain from calling Release.
//
// Why pool/bufp are on the struct rather than a `func()` field: a
// func-field that captures the pool pointer escapes to the heap on
// every recv, defeating the point of pooling. With raw pointer fields
// the recv loop is zero-alloc on the hot path.
type Packet struct {
	From    net.Addr
	Payload []byte
	pool    *sync.Pool
	bufp    *[]byte
}

// Release returns Payload's backing buffer to the originating
// transport's pool (if any). Idempotent.
func (p *Packet) Release() {
	if p.pool == nil || p.bufp == nil {
		return
	}
	// Restore full capacity so the next consumer of this pool slot sees
	// the entire buffer, not the slice limited to last packet's `n`.
	*p.bufp = (*p.bufp)[:cap(*p.bufp)]
	p.pool.Put(p.bufp)
	p.pool = nil
	p.bufp = nil
	p.Payload = nil
}

// Transport is the connectionless packet pipe used by the DHT, signaling
// layer, and presence publisher.
type Transport interface {
	// LocalAddr is the address peers should use to reach this transport.
	LocalAddr() net.Addr

	// Dial parses a textual address (the form returned by net.Addr.String)
	// back into a net.Addr suitable for Send. Implementations are expected
	// to be cheap; the result is not a connection.
	Dial(addr string) (net.Addr, error)

	// Send writes payload to `to`. The context bounds the operation —
	// implementations should respect cancellation.
	Send(ctx context.Context, to net.Addr, payload []byte) error

	// Inbox delivers received packets. The channel is closed by Close.
	Inbox() <-chan Packet

	// Close releases resources. Subsequent operations return ErrClosed.
	Close() error
}
