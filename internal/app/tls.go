// Package app holds the run-loop orchestration for the udisend
// messenger binary: load profile, open the network/storage/messenger
// stack, wire HTTP UI, supervise via errgroup. cmd/messenger is the
// thin CLI wrapper; this package is where the work happens, kept here
// (not in pkg/) because every choice is project-specific.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme/autocert"

	tspkg "github.com/udisondev/udisend/pkg/tailscale"
)

// ACMEServer pairs an autocert.Manager with a plain HTTP server on :80
// that handles the HTTP-01 challenge and redirects everything else to
// HTTPS. Cert lifecycle (issue, renew) is autocert's job; this type
// only owns the challenge endpoint and its lifetime.
type ACMEServer struct {
	manager *autocert.Manager
	server  *http.Server
}

// NewACMEServer builds the manager + :80 challenge server. dir is a
// directory where issued certs are cached so a restart doesn't
// re-issue (and hit Let's Encrypt rate limits).
func NewACMEServer(host, dir string) *ACMEServer {
	m := &autocert.Manager{
		Cache:      autocert.DirCache(dir),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(host),
	}
	srv := &http.Server{
		Addr:              ":80",
		Handler:           m.HTTPHandler(nil),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	return &ACMEServer{manager: m, server: srv}
}

// TLSConfig returns the *tls.Config to plug into the HTTPS server.
// Hardened to TLS 1.2+, modern curves; cipher suites left to stdlib's
// safe defaults.
func (a *ACMEServer) TLSConfig() *tls.Config {
	cfg := a.manager.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	cfg.CurvePreferences = []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}

	return cfg
}

// Run blocks until ctx cancels or ListenAndServe fails. Returns nil on
// the graceful-shutdown path so callers can errors.Is/Canceled-filter
// it.
func (a *ACMEServer) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		// Detached from ctx on purpose: the parent has already been
		// cancelled, so propagating it would skip the graceful drain
		// of any in-flight challenge request.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.server.Shutdown(shutdownCtx)
	}()

	err := a.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

// TailscaleCertRefresher fetches and periodically refreshes a TLS
// certificate from tailscaled. Tailscale's daemon caches and renews
// the LE-issued cert under the hood; we just have to call it again
// every so often to pick up fresh material.
type TailscaleCertRefresher struct {
	client *tspkg.Client
	host   string
	logger *slog.Logger
	cert   atomic.Pointer[tls.Certificate]
}

// NewTailscaleCertRefresher constructs a refresher bound to host. The
// caller is expected to invoke Fetch once for fail-fast at startup,
// then Run for periodic refresh.
func NewTailscaleCertRefresher(c *tspkg.Client, host string, logger *slog.Logger) *TailscaleCertRefresher {
	return &TailscaleCertRefresher{client: c, host: host, logger: logger}
}

// Fetch synchronously requests the cert and stashes it. Used for the
// startup fail-fast and for periodic refresh.
func (t *TailscaleCertRefresher) Fetch(ctx context.Context) error {
	cert, err := t.client.CertWithDeadline(ctx, t.host, 90*time.Second)
	if err != nil {
		return err
	}
	t.cert.Store(cert)
	return nil
}

// TLSConfig returns a *tls.Config whose GetCertificate reads from the
// atomic pointer. Old connections holding a previous Certificate keep
// working; new handshakes pick up the freshest material.
func (t *TailscaleCertRefresher) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			c := t.cert.Load()
			if c == nil {
				return nil, errors.New("tailscale cert not available yet")
			}
			return c, nil
		},
	}
}

// Run loops every 12h calling Fetch until ctx is cancelled. LE certs
// are 90 days; 12h is small enough that the tailscaled-side rotation
// (which happens at ~T-30d) is reflected within half a day.
func (t *TailscaleCertRefresher) Run(ctx context.Context) {
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := t.Fetch(ctx); err != nil {
				t.logger.Warn("tailscale: cert refresh failed (will retry next tick)", "err", err)
			}
		}
	}
}
