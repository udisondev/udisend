// Package turn provides a thin wrapper around pion/turn/v4 sufficient to
// run an embedded TURN relay inside a udisend network node. Auth follows
// RFC 7635 ephemeral credentials (username = "<expiry-unix>:<user>",
// password = HMAC-SHA1(secret, username)) and is gated by a per-IP token
// bucket against abuse.
package turn

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	pionturn "github.com/pion/turn/v4"

	"github.com/udisondev/udisend/pkg/ratelimit"
)

// DefaultRealm is the realm advertised in TURN responses when
// Config.Realm is empty. Clients echo it back when supplying
// credentials. Override per-instance via Config.Realm — the constant
// stays for tests and external consumers that prefer the udisend
// default.
const DefaultRealm = "udisend"

// Realm is retained for backward compatibility with code that compared
// against the package-level constant. Prefer Config.Realm.
//
// Deprecated: use Config.Realm + DefaultRealm instead.
const Realm = DefaultRealm

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
	// Clients derive ephemeral credentials from it (username =
	// "<expiry>:<user>", password = HMAC(secret, username)).
	SharedSecret string

	// Realm is the long-term-credentials realm advertised in
	// challenges. Empty defaults to DefaultRealm ("udisend"). External
	// consumers reusing pkg/turn for their own service set this to
	// their own realm name.
	Realm string

	// MaxCredentialLifetime caps how far in the future a client may set
	// its username's expiry timestamp. 24h by default. Older expiries are
	// rejected outright.
	MaxCredentialLifetime time.Duration

	// AuthRate / AuthBurst control the per-source-IP token bucket on auth
	// attempts. Defaults: 5 attempts/sec, burst 10. Zero rate disables
	// limiting.
	AuthRate  float64
	AuthBurst float64
}

// Defaults — tuned conservative for an MVP volunteer relay.
const (
	DefaultMaxCredentialLifetime = 24 * time.Hour
	DefaultAuthRate              = 5
	DefaultAuthBurst             = 10
)

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

	if cfg.MaxCredentialLifetime <= 0 {
		cfg.MaxCredentialLifetime = DefaultMaxCredentialLifetime
	}
	if cfg.AuthRate == 0 {
		cfg.AuthRate = DefaultAuthRate
	}
	if cfg.AuthBurst == 0 {
		cfg.AuthBurst = DefaultAuthBurst
	}
	if cfg.Realm == "" {
		cfg.Realm = DefaultRealm
	}
	realm := cfg.Realm
	limiter := ratelimit.New(cfg.AuthRate, cfg.AuthBurst)

	server, err := pionturn.NewServer(pionturn.ServerConfig{
		Realm: realm,
		AuthHandler: func(username, requestedRealm string, srcAddr net.Addr) ([]byte, bool) {
			if !limiter.Allow(authKey(srcAddr)) {
				return nil, false
			}
			if requestedRealm != "" && requestedRealm != realm {
				return nil, false
			}
			if !validUsername(username, cfg.MaxCredentialLifetime) {
				return nil, false
			}
			password := computePassword(cfg.SharedSecret, username)
			return pionturn.GenerateAuthKey(username, realm, password), true
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

// validUsername parses an RFC 7635-style ephemeral TURN username
// "<expiry-unix>:<user>" and rejects malformed, expired, or
// far-future entries (cap = maxLifetime past now). The user portion
// must be non-empty so an attacker cannot drive the credential cache
// with `123:` collisions.
func validUsername(username string, maxLifetime time.Duration) bool {
	colon := strings.IndexByte(username, ':')
	if colon <= 0 {
		return false
	}
	if colon+1 >= len(username) {
		return false
	}
	if len(username) > 256 {
		return false
	}
	exp, err := strconv.ParseInt(username[:colon], 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	if exp <= now {
		return false
	}
	if exp > now+int64(maxLifetime/time.Second) {
		return false
	}
	return true
}

// authKey extracts the rate-limit bucket key from a source address.
func authKey(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	if u, ok := addr.(*net.UDPAddr); ok && u.IP != nil {
		return u.IP.String()
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// computePassword derives the RFC 7635 ephemeral password
// (base64(HMAC-SHA1(secret, username))) — same value the client must
// present and the same value the server uses to derive the auth key.
func computePassword(secret, username string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// EphemeralCredential builds the username/password pair a TURN client
// should present given the shared secret and a desired expiry. The
// password is the base64-encoded HMAC-SHA1; the client passes it as the
// raw `Password` string, and pion will internally key-derive it the
// same way the server does in AuthHandler.
func EphemeralCredential(secret, user string, expiry time.Time) (username, password string) {
	username = fmt.Sprintf("%d:%s", expiry.Unix(), user)
	password = computePassword(secret, username)
	return
}
