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
)

// Server is a STUN binding-request responder bound to a UDP socket.
type Server struct {
	conn *net.UDPConn

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
	return &Server{conn: conn, closed: make(chan struct{})}, nil
}

// LocalAddr returns the bound address.
func (s *Server) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// Run blocks while serving. Returns once ctx is cancelled or Close is called.
func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.conn.SetReadDeadline(timeBeforeNow())
	}()
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
