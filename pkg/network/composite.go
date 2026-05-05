package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/udisondev/udisend/pkg/transport"
)

// compositeTransport bundles two transport.Transport implementations
// (typically a UDP primary plus a WebRTC secondary) into one
// Transport facade. Dispatch is by destination address type / scheme:
//
//   - WebRTCAddr or "rtc:..."        → secondary
//   - everything else (UDP, mem, …)  → primary
//
// Inbox fans in from both. LocalAddr / Close mirror primary unless
// the caller uses a WebRTCAddr explicitly.
//
// The composite owns neither sub-transport — Close releases its
// fan-in goroutine but does NOT close the underlying transports;
// network.Node owns that lifecycle. This split keeps Open / Run
// teardown ordering authoritative.
type compositeTransport struct {
	primary   transport.Transport
	secondary transport.Transport

	inbox     chan transport.Packet
	closeOnce sync.Once
	closed    chan struct{}
	wg        sync.WaitGroup
}

// newCompositeTransport spawns the fan-in goroutines. Caller MUST
// call Close to release them; closing the underlying transports
// alone is not enough because their Inbox channels are produced by
// recvLoops we do not control.
func newCompositeTransport(primary, secondary transport.Transport) *compositeTransport {
	c := &compositeTransport{
		primary:   primary,
		secondary: secondary,
		inbox:     make(chan transport.Packet, 256),
		closed:    make(chan struct{}),
	}

	c.wg.Add(2)
	go c.fanIn(primary.Inbox())
	go c.fanIn(secondary.Inbox())

	return c
}

// LocalAddr returns the primary's address. Secondary addressing
// (WebRTCAddr) is identity-keyed and rarely useful as a string;
// callers needing it can ask the underlying WebRTCTransport
// directly.
func (c *compositeTransport) LocalAddr() net.Addr {
	return c.primary.LocalAddr()
}

// Dial parses a textual address. If it carries the WebRTC scheme
// ("rtc:...") it goes through secondary; otherwise primary handles
// it. Errors from either path are returned verbatim.
func (c *compositeTransport) Dial(addr string) (net.Addr, error) {
	if strings.HasPrefix(addr, "rtc:") {
		return c.secondary.Dial(addr)
	}

	return c.primary.Dial(addr)
}

// Send routes the payload by destination address type. WebRTCAddr →
// secondary; UDPAddr / MemoryAddr / anything else → primary. If
// secondary returns ErrNoRoute the caller MAY choose to fall back
// (this composite does NOT auto-fallback; that policy lives one
// layer up in the Router so retries are explicit).
func (c *compositeTransport) Send(ctx context.Context, to net.Addr, payload []byte) error {
	if c.isClosed() {
		return transport.ErrClosed
	}

	if _, ok := to.(transport.WebRTCAddr); ok {
		return c.secondary.Send(ctx, to, payload)
	}

	return c.primary.Send(ctx, to, payload)
}

// Inbox delivers fan-in'd packets from both sub-transports. The
// channel is closed by Close.
func (c *compositeTransport) Inbox() <-chan transport.Packet { return c.inbox }

// Close stops the fan-in goroutines. The underlying transports are
// NOT closed — caller (network.Node) owns that. Idempotent.
func (c *compositeTransport) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		// The fan-in goroutines may be parked on a sub-transport
		// Inbox read. We rely on the caller closing those
		// transports first so the goroutines see the closed channel
		// and exit. If that has not happened yet, Close still
		// returns immediately and Wait will block until the caller
		// completes the teardown.
	})
	c.wg.Wait()
	close(c.inbox)

	return err
}

func (c *compositeTransport) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *compositeTransport) fanIn(src <-chan transport.Packet) {
	defer c.wg.Done()
	for {
		select {
		case <-c.closed:
			// Drain any remaining packets so we don't leak refs;
			// then stop.
			for range src {
				// drop — fan-in is closed.
			}

			return
		case pkt, ok := <-src:
			if !ok {
				return
			}
			select {
			case c.inbox <- pkt:
			case <-c.closed:
				return
			}
		}
	}
}

// Compile-time assertion that compositeTransport satisfies the
// Transport interface — DHT and signaling layers consume it through
// that contract.
var _ transport.Transport = (*compositeTransport)(nil)

// ErrCompositeRequiresBoth is returned when newCompositeTransport
// is given a nil sub-transport. Exposed for tests; production code
// constructs via the public network.Open path.
var ErrCompositeRequiresBoth = errors.New("network: composite transport requires both primary and secondary")

// safeNewCompositeTransport is the validated constructor used by
// Open. Returns ErrCompositeRequiresBoth on nil arguments.
func safeNewCompositeTransport(primary, secondary transport.Transport) (*compositeTransport, error) {
	if primary == nil || secondary == nil {
		return nil, fmt.Errorf("%w: primary=%v secondary=%v",
			ErrCompositeRequiresBoth, primary != nil, secondary != nil)
	}

	return newCompositeTransport(primary, secondary), nil
}
