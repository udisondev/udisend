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
)

// MaxPacketSize is the cap enforced on every Send and Receive. Anything
// larger is rejected before it touches the network.
const MaxPacketSize = 64 * 1024

// ErrClosed is returned from Send / Receive after Close has been called.
var ErrClosed = errors.New("transport: closed")

// Packet is an inbound datagram delivered via Inbox().
type Packet struct {
	From    net.Addr
	Payload []byte
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
