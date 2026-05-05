package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// acmeChallengeServer pairs an autocert.Manager with a plain HTTP server
// on :80 that handles the HTTP-01 challenge and redirects everything
// else to HTTPS. Cert lifecycle (issue, renew) is autocert's job; this
// type only owns the challenge endpoint and its lifetime.
type acmeChallengeServer struct {
	manager *autocert.Manager
	server  *http.Server
}

// newACME builds the manager + :80 challenge server. dir is a directory
// where issued certs are cached so a restart doesn't re-issue (and hit
// Let's Encrypt rate limits).
func newACME(host, dir string) *acmeChallengeServer {
	m := &autocert.Manager{
		Cache:      autocert.DirCache(dir),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(host),
		// 30 days before expiry is autocert's default; we leave it.
	}
	srv := &http.Server{
		Addr:              ":80",
		Handler:           m.HTTPHandler(nil),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	return &acmeChallengeServer{manager: m, server: srv}
}

// TLSConfig returns the *tls.Config to plug into the HTTPS server.
// Hardened to TLS 1.2+, modern curves; cipher suites left to stdlib's
// safe defaults.
func (a *acmeChallengeServer) TLSConfig() *tls.Config {
	cfg := a.manager.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	cfg.CurvePreferences = []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}

	return cfg
}

// Run blocks until ctx cancels or ListenAndServe fails. Returns nil on
// the graceful-shutdown path so callers can errors.Is/Canceled.
func (a *acmeChallengeServer) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
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
