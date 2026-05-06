// Package tailscale wraps the local tailscaled API for udisend's use.
// We need only three things: detect tailscaled is running and logged in,
// learn this device's MagicDNS hostname, and obtain a TLS certificate
// for that hostname (issued by Let's Encrypt via Tailscale's ACME pipe).
//
// The wrapper is minimal on purpose — udisend treats Tailscale as one
// of several deployment modes; the rest of the codebase shouldn't have
// to import tailscale.com directly.
package tailscale

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	tslocal "tailscale.com/client/local"
)

// ErrNotRunning is returned by Detect when tailscaled is unreachable
// (not installed, not running, or socket-permission denied).
var ErrNotRunning = errors.New("tailscale: daemon not reachable")

// ErrNotLoggedIn is returned when tailscaled is running but the user
// has not logged in (`tailscale up` not done yet).
var ErrNotLoggedIn = errors.New("tailscale: not logged in")

// ErrNoMagicDNS is returned when the device has no MagicDNS hostname —
// either MagicDNS is disabled in the tailnet, or the device has no Self
// info yet (very early after start).
var ErrNoMagicDNS = errors.New("tailscale: device has no MagicDNS name")

// Client is a thin facade over tailscale.com/client/local.Client.
type Client struct {
	lc *tslocal.Client
}

// New returns a Client that talks to the host's local tailscaled. No
// I/O happens here — call Detect to verify availability.
func New() *Client {
	return &Client{lc: &tslocal.Client{}}
}

// Detect verifies tailscaled is reachable AND logged in AND has a
// MagicDNS hostname. The most-specific sentinel error is returned so
// callers can render an actionable message.
func (c *Client) Detect(ctx context.Context) error {
	st, err := c.lc.Status(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	if st.BackendState != "Running" {
		return fmt.Errorf("%w (state=%s, run `tailscale up`)", ErrNotLoggedIn, st.BackendState)
	}
	if st.Self == nil || st.Self.DNSName == "" {
		return ErrNoMagicDNS
	}
	return nil
}

// Hostname returns this device's FQDN on the tailnet, without trailing
// dot. Example: "udisend-host.fluffy-otter.ts.net". Calls Status under
// the hood — call Detect first if you want clearer error semantics.
func (c *Client) Hostname(ctx context.Context) (string, error) {
	st, err := c.lc.Status(ctx)
	if err != nil {
		return "", fmt.Errorf("tailscale: status: %w", err)
	}
	if st.Self == nil || st.Self.DNSName == "" {
		return "", ErrNoMagicDNS
	}

	return strings.TrimSuffix(st.Self.DNSName, "."), nil
}

// Cert obtains a TLS certificate for the given tailnet hostname.
// Tailscale's daemon handles the ACME dance with Let's Encrypt under
// the hood — repeated calls return the cached cert until it nears
// expiry, at which point Tailscale auto-renews. Failure here usually
// means HTTPS-Certs isn't enabled in the tailnet's admin console.
func (c *Client) Cert(ctx context.Context, host string) (*tls.Certificate, error) {
	certPEM, keyPEM, err := c.lc.CertPair(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("tailscale: cert pair for %q: %w", host, err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tailscale: parse cert: %w", err)
	}

	return &cert, nil
}

// CertWithDeadline is Cert with a per-call timeout. ACME issuance can
// take 30-60s on first request (cold issuance); we want callers to be
// able to cap it without leaking goroutines.
func (c *Client) CertWithDeadline(parent context.Context, host string, d time.Duration) (*tls.Certificate, error) {
	ctx, cancel := context.WithTimeout(parent, d)
	defer cancel()

	return c.Cert(ctx, host)
}
