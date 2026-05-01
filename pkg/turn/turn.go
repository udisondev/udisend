// Package turn provides a thin wrapper around pion/turn/v4 sufficient to
// run an embedded TURN relay inside a udisend network node. Auth uses
// long-term-credentials with a shared static realm — adequate for
// localhost demos but NOT production. See ASSUMPTIONS.md for the
// hardening checklist (per-IP rate-limit, abuse mitigations).
package turn

import (
	"context"
	"errors"
	"fmt"
	"net"

	pionturn "github.com/pion/turn/v4"
)

// Realm is the static realm advertised in TURN responses. Clients echo
// it back when supplying credentials.
const Realm = "udisend"

// Server wraps a pion/turn server with a single UDP listener.
type Server struct {
	server *pionturn.Server
	conn   net.PacketConn
}

// Config configures NewServer.
type Config struct {
	// PublicIP is what the server uses for the TURN allocation candidate
	// addresses. Use the public-facing IP of the host. For localhost
	// demos, "127.0.0.1".
	PublicIP string

	// ListenAddr is the UDP address to bind, e.g. "0.0.0.0:3478" or
	// "127.0.0.1:0".
	ListenAddr string

	// SharedSecret is the static TURN long-term-credentials secret.
	// Clients derive a username/password from this. For overnight scope
	// we accept any username paired with HMAC-SHA1(SharedSecret, username).
	SharedSecret string
}

// NewServer binds the TURN listener and registers the auth handler. Call
// Close to release resources.
func NewServer(cfg Config) (*Server, error) {
	if cfg.PublicIP == "" {
		return nil, errors.New("turn: PublicIP required")
	}
	if cfg.ListenAddr == "" {
		return nil, errors.New("turn: ListenAddr required")
	}
	if cfg.SharedSecret == "" {
		return nil, errors.New("turn: SharedSecret required")
	}
	conn, err := net.ListenPacket("udp4", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("turn: listen: %w", err)
	}
	publicIP := net.ParseIP(cfg.PublicIP)
	if publicIP == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("turn: invalid public IP %q", cfg.PublicIP)
	}

	server, err := pionturn.NewServer(pionturn.ServerConfig{
		Realm: Realm,
		AuthHandler: func(username, _ string, _ net.Addr) ([]byte, bool) {
			key := pionturn.GenerateAuthKey(username, Realm, cfg.SharedSecret)
			return key, true
		},
		PacketConnConfigs: []pionturn.PacketConnConfig{
			{
				PacketConn: conn,
				RelayAddressGenerator: &pionturn.RelayAddressGeneratorStatic{
					RelayAddress: publicIP,
					Address:      "0.0.0.0",
				},
			},
		},
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("turn: server: %w", err)
	}
	return &Server{server: server, conn: conn}, nil
}

// LocalAddr returns the UDP address the server is listening on.
func (s *Server) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// Run blocks while serving. Returns when ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	<-ctx.Done()
	return s.server.Close()
}

// Close stops the server.
func (s *Server) Close() error { return s.server.Close() }
