package httpui

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/udisondev/udisend/internal/messenger"
)

// TestValidatePublicModeConfig pins the contract for the four supported
// deployment modes: loopback bind only ok when TrustProxy=true; TLS or
// TrustProxy required in public mode; TLSCert/TLSKey and TLSConfig are
// mutually exclusive (autocert fork must clear cert files).
func TestValidatePublicModeConfig(t *testing.T) {
	t.Parallel()

	emptyTLSCfg := &tls.Config{} // marker for "TLSConfig present"
	mngr := &messenger.Messenger{}

	tests := []struct {
		name    string
		cfg     Config
		wantErr string // substring; "" = expect ok
	}{
		{
			name: "private (Public=false) ok regardless",
			cfg:  Config{Messenger: mngr, Listen: "127.0.0.1:0", Public: false},
		},
		{
			name:    "public + loopback + no proxy rejected",
			cfg:     Config{Messenger: mngr, Listen: "127.0.0.1:8443", Public: true, TLSCert: "x", TLSKey: "y"},
			wantErr: "non-loopback",
		},
		{
			name: "public + loopback + TrustProxy ok (Caddy in front)",
			cfg:  Config{Messenger: mngr, Listen: "127.0.0.1:8443", Public: true, TrustProxy: true},
		},
		{
			name:    "public + non-loopback + no TLS no proxy rejected",
			cfg:     Config{Messenger: mngr, Listen: "0.0.0.0:8443", Public: true},
			wantErr: "requires TLS",
		},
		{
			name: "public + non-loopback + TLS files ok",
			cfg:  Config{Messenger: mngr, Listen: "0.0.0.0:8443", Public: true, TLSCert: "x", TLSKey: "y"},
		},
		{
			name: "public + non-loopback + TLSConfig (autocert) ok",
			cfg:  Config{Messenger: mngr, Listen: "0.0.0.0:443", Public: true, TLSConfig: emptyTLSCfg},
		},
		{
			name:    "TLSCert + TLSConfig mutually exclusive",
			cfg:     Config{Messenger: mngr, Listen: "0.0.0.0:443", Public: true, TLSCert: "x", TLSKey: "y", TLSConfig: emptyTLSCfg},
			wantErr: "mutually exclusive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePublicModeConfig(tt.cfg)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

// TestSecureHeaders_HSTSGate locks down the HSTS-emission rules: HSTS
// MUST appear when the server terminates TLS in-process (regardless of
// whether that's via cert files OR via cfg.TLSConfig from autocert) AND
// when an upstream proxy advertises HTTPS via X-Forwarded-Proto. It
// MUST NOT appear over plaintext loopback.
func TestSecureHeaders_HSTSGate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		publicMode       bool
		hasTLS           bool
		xForwardedProto  string
		wantHSTS         bool
	}{
		{name: "loopback (no public, no tls) — no HSTS", publicMode: false, hasTLS: false, wantHSTS: false},
		{name: "public + tls cert — HSTS", publicMode: true, hasTLS: true, wantHSTS: true},
		{name: "public + autocert (TLSConfig) — HSTS", publicMode: true, hasTLS: true, wantHSTS: true},
		{name: "public + trust-proxy + XFP=https — HSTS", publicMode: true, hasTLS: false, xForwardedProto: "https", wantHSTS: true},
		{name: "public + trust-proxy + XFP=http — no HSTS", publicMode: true, hasTLS: false, xForwardedProto: "http", wantHSTS: false},
		{name: "public + no tls + no XFP — no HSTS", publicMode: true, hasTLS: false, wantHSTS: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{publicMode: tt.publicMode, hasTLS: tt.hasTLS}
			h := s.secureHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			req := httptest.NewRequest(http.MethodGet, "https://x/", nil)
			if tt.xForwardedProto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.xForwardedProto)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			got := rr.Header().Get("Strict-Transport-Security")
			if (got != "") != tt.wantHSTS {
				t.Errorf("HSTS header: got %q, wantPresent=%v", got, tt.wantHSTS)
			}
			if tt.wantHSTS && !strings.Contains(got, "max-age=31536000") {
				t.Errorf("HSTS missing max-age: %q", got)
			}
		})
	}
}
