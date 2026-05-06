// Package stun provides a minimal embedded STUN server suitable for use
// inside a udisend network node. The server speaks RFC 5389 binding
// requests so WebRTC clients can discover their server-reflexive
// (srflx) candidates.
//
// This is a thin wrapper around pion/stun/v3: we accept UDP datagrams,
// classify them as STUN binding requests, and respond with the source
// IP:Port of the request. Anything that doesn't parse as a binding
// request is dropped.
package stun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/pion/stun/v3"

	"github.com/udisondev/udisend/pkg/ratelimit"
)

// DefaultBindingRatePerIP caps how many STUN binding-success responses
// a single source IP receives per second. STUN responses run ~80 bytes
// against ~20-byte requests — a 4× amplification factor that public
// reflectors are routinely abused for DDoS (NETSCOUT 2024 reports 75K
// servers in active abuse). Each Binding request consumes one token;
// a sustained spray from one IP gets clipped to this rate. Legitimate
// WebRTC clients fire one or two binding requests per session, so the
// cap is far above any honest workload.
const DefaultBindingRatePerIP = 30

// DefaultBindingBurstPerIP allows short bursts when a multi-NAT client
// re-fires several requests to discover its public mapping. Two seconds
// of burst keeps real workloads working under sub-second jitter.
const DefaultBindingBurstPerIP = 60

// Server is a STUN binding-request responder bound to a UDP socket.
type Server struct {
	conn *net.UDPConn
	// limiter throttles binding-success responses per source IP. Refills
	// at DefaultBindingRatePerIP tokens/sec with a DefaultBindingBurstPerIP
	// burst; over-budget requests are silently dropped (we don't even
	// emit an error response — that would itself be amplification).
	limiter *ratelimit.Limiter

	closeOnce sync.Once
	closed    chan struct{}
}

// Listen binds a STUN server on addr (e.g. "0.0.0.0:3478"). Use ":0"
// to let the kernel pick.
func Listen(addr string) (*Server, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("stun: resolve %q: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("stun: listen %q: %w", addr, err)
	}
	return &Server{
		conn:    conn,
		limiter: ratelimit.New(DefaultBindingRatePerIP, DefaultBindingBurstPerIP),
		closed:  make(chan struct{}),
	}, nil
}

// LocalAddr returns the bound address.
func (s *Server) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// Run blocks while serving. Returns once ctx is cancelled or Close is called.
func (s *Server) Run(ctx context.Context) error {
	// On ctx cancellation kick the blocking Read out of its deadline.
	// AfterFunc replaces a long-lived watcher goroutine: the runtime
	// schedules the callback only if/when ctx is cancelled, and stop()
	// races cleanly with normal Close so we never leak the timer.
	stop := context.AfterFunc(ctx, func() {
		_ = s.conn.SetReadDeadline(timeBeforeNow())
	})
	defer stop()

	buf := make([]byte, 1500)
	for {
		select {
		case <-s.closed:
			return nil
		case <-ctx.Done():
			return nil
		default:
		}
		n, src, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// timed-out due to ctx.Done above
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			continue
		}
		if !stun.IsMessage(buf[:n]) {
			continue
		}
		req := &stun.Message{Raw: append([]byte{}, buf[:n]...)}
		if err := req.Decode(); err != nil {
			continue
		}
		if req.Type.Method != stun.MethodBinding || req.Type.Class != stun.ClassRequest {
			continue
		}
		// Per-IP rate-limit BEFORE building the response — rejecting
		// over-quota requests with silence prevents the server from
		// participating in reflective DDoS amplification regardless of
		// how many spoofed binding requests an attacker emits.
		if src.IP != nil && !s.limiter.Allow(src.IP.String()) {
			continue
		}
		resp, err := stun.Build(
			stun.NewTransactionIDSetter(req.TransactionID),
			stun.BindingSuccess,
			&stun.XORMappedAddress{IP: src.IP, Port: src.Port},
			stun.NewSoftware("udisend-stun"),
			stun.Fingerprint,
		)
		if err != nil {
			continue
		}
		_, _ = s.conn.WriteToUDP(resp.Raw, src)
	}
}

// Close terminates the server.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.closed)
		err = s.conn.Close()
	})
	return err
}
