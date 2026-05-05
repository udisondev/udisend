package httpui

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
)

// validatePublicModeConfig enforces the cmd-level startup invariants at
// the package boundary so any future caller (test, future binary,
// embedding) is fail-closed regardless of cmd/messenger's own checks.
//
//   - Public + loopback bind: rejected (loopback should use the URL-token
//     flow, not session cookies).
//   - Public without TLS material AND without TrustProxy: rejected
//     (passphrase MUST not flow over plaintext).
func validatePublicModeConfig(cfg Config) error {
	if !cfg.Public {
		return nil
	}
	if isLoopbackBind(cfg.Listen) {
		return errors.New("httpui: public mode requires a non-loopback listen address")
	}
	hasInlineTLS := cfg.TLSCert != "" && cfg.TLSKey != ""
	if !hasInlineTLS && !cfg.TrustProxy {
		return errors.New("httpui: public mode requires TLS (-tls-cert + -tls-key) or -trust-proxy with a TLS-terminating reverse proxy")
	}

	return nil
}

// isLoopbackBind tests whether the bind address is on a loopback
// interface. A bare port (":9000") is treated as non-loopback (binds to
// 0.0.0.0 / [::]); an unparseable address is treated as non-loopback so
// public-mode validation runs in the conservative direction.
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	return ip.IsLoopback()
}

// checkHost rejects requests whose Host header is not in the allowlist.
// This is the DNS-rebinding defence: an attacker who lures the user to
// evil.attacker.com (TTL=1, eventually resolved to 127.0.0.1) reaches the
// loopback listener with `Host: evil.attacker.com`. Without this check
// the only defence is the X-Requested-With CSRF gate, which a same-origin
// XHR from the rebound page satisfies trivially.
//
// Allowed hosts (loopback mode):
//   - 127.0.0.1[:port], localhost[:port], [::1][:port]
//
// Allowed hosts (public mode):
//   - the same loopback set (so health-checks via `localhost` still work
//     when bound to 0.0.0.0 behind a proxy on the same box)
//   - publicHost[:port], either lower- or mixed-case
//
// 421 Misdirected Request is the spec-correct status for a request that
// arrived at a server that is not configured to produce a response for
// the combination of scheme + authority — RFC 9110 § 15.5.20.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAllowedHost(r.Host) {
			http.Error(w, "bad host", http.StatusMisdirectedRequest)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isAllowedHost evaluates a Host-header value against the active allowlist.
// Returns false for empty input — HTTP/1.1 requires Host, and HTTP/2
// synthesizes one from :authority.
func (s *Server) isAllowedHost(rawHost string) bool {
	if rawHost == "" {
		return false
	}
	host := rawHost
	if h, _, err := net.SplitHostPort(rawHost); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")

	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	if s.publicMode && s.publicHost != "" {
		ph := s.publicHost
		if h, _, err := net.SplitHostPort(ph); err == nil {
			ph = h
		}
		if strings.EqualFold(strings.TrimSuffix(strings.TrimPrefix(ph, "["), "]"), host) {
			return true
		}
	}

	return false
}

// secureHeaders wraps a handler and emits the project-wide security
// header set on every response. The values are tuned for a same-origin
// SPA that loads only its own embedded assets and never iframes itself.
//
// Notes on individual headers:
//
//   - CSP: default-src 'self' kills any inline-eval / cross-origin
//     dependency; img-src adds data: for the avatar gradients; connect-src
//     'self' covers EventSource + fetch; frame-ancestors 'none' is the
//     non-deprecated successor of X-Frame-Options.
//   - Permissions-Policy explicitly DISables features we never use, so
//     a future XSS cannot opt-in via document API.
//   - X-Frame-Options retained for legacy browsers that ignore CSP
//     frame-ancestors.
//   - HSTS only emitted in public mode AND only when the request was
//     served over TLS (in-process or upstream proxy). 1y + includeSubDomains.
//   - Referrer-Policy: no-referrer kills the loopback-token leak via
//     outbound link clicks.
//
// `unsafe-inline` for style-src is required because the SPA inlines a
// large stylesheet; once we move it to a hash-pinned external stylesheet
// we can drop it. unsafe-inline for script-src is NOT included — JS is
// loaded as `type="module"` from /app.js only.
func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if _, ok := h["Content-Security-Policy"]; !ok {
			h.Set("Content-Security-Policy", strings.Join([]string{
				"default-src 'self'",
				"img-src 'self' data:",
				"style-src 'self' 'unsafe-inline'",
				"script-src 'self'",
				"connect-src 'self'",
				"font-src 'self'",
				"media-src 'self' blob:",
				"frame-ancestors 'none'",
				"base-uri 'self'",
				"form-action 'self'",
				"object-src 'none'",
			}, "; "))
		}
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "geolocation=(), payment=(), usb=(), magnetometer=(), accelerometer=(), gyroscope=()")
		if s.publicMode && (s.tlsCert != "" || strings.HasPrefix(r.Header.Get("X-Forwarded-Proto"), "https")) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

// noStoreOn applies Cache-Control: no-store to authenticated API
// responses that may contain identity hashes, contact lists, message
// content, IPs, or session ids. Public-mode setups behind a misconfigured
// reverse-proxy or shared-cache could otherwise persist that data.
func noStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

// hardenedTLSConfig pins TLS 1.2 as the floor (1.3 wherever supported)
// and selects modern curves. Cipher suites are left to the Go runtime
// default — for TLS 1.2 stdlib excludes the broken ones, for TLS 1.3
// the spec mandates safe ones.
func hardenedTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:               tls.VersionTLS12,
		CurvePreferences:         []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
		PreferServerCipherSuites: true,
	}
}
