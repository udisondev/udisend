package main

import (
	"strings"
	"testing"
)

func TestProfileValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		p        profile
		hasCreds bool
		wantErr  string // substring; "" = no error expected
	}{
		{
			name:     "loopback ok",
			p:        profile{Mode: modeLoopback, BindHTTP: "127.0.0.1:0", BindP2P: "127.0.0.1:0"},
			hasCreds: false,
			wantErr:  "",
		},
		{
			name:    "loopback with non-loopback bind rejected",
			p:       profile{Mode: modeLoopback, BindHTTP: "0.0.0.0:8443", BindP2P: "127.0.0.1:0"},
			wantErr: "loopback bind_http",
		},
		{
			name: "lan-ip without creds",
			p: profile{
				Mode:       modeLANIP,
				BindHTTP:   "0.0.0.0:8443",
				BindP2P:    "0.0.0.0:9000",
				PublicHost: "192.168.0.105:8443",
				TLSCert:    "/x/c", TLSKey: "/x/k",
			},
			hasCreds: false,
			wantErr:  "passphrase",
		},
		{
			name: "lan-ip without public host",
			p: profile{
				Mode: modeLANIP, BindHTTP: "0.0.0.0:8443", BindP2P: "0.0.0.0:9000",
				TLSCert: "/x/c", TLSKey: "/x/k",
			},
			hasCreds: true,
			wantErr:  "public_host",
		},
		{
			name: "lan-ip without TLS",
			p: profile{
				Mode: modeLANIP, BindHTTP: "0.0.0.0:8443", BindP2P: "0.0.0.0:9000",
				PublicHost: "192.168.0.105:8443",
			},
			hasCreds: true,
			wantErr:  "tls_cert",
		},
		{
			name: "lan-ip ok",
			p: profile{
				Mode: modeLANIP, BindHTTP: "0.0.0.0:8443", BindP2P: "0.0.0.0:9000",
				PublicHost: "192.168.0.105:8443",
				TLSCert:    "/x/c", TLSKey: "/x/k",
			},
			hasCreds: true,
		},
		{
			name: "public-autocert ok",
			p: profile{
				Mode: modePublicAutocert, BindHTTP: "0.0.0.0:443", BindP2P: "0.0.0.0:9000",
				PublicHost: "udisend.example.com",
			},
			hasCreds: true,
		},
		{
			name: "public-autocert with TLS files rejected",
			p: profile{
				Mode: modePublicAutocert, BindHTTP: "0.0.0.0:443", BindP2P: "0.0.0.0:9000",
				PublicHost: "udisend.example.com",
				TLSCert:    "/x/c", TLSKey: "/x/k",
			},
			hasCreds: true,
			wantErr:  "forbids tls_cert/tls_key",
		},
		{
			name: "public-autocert with trust_proxy rejected",
			p: profile{
				Mode: modePublicAutocert, BindHTTP: "0.0.0.0:443", BindP2P: "0.0.0.0:9000",
				PublicHost: "udisend.example.com",
				TrustProxy: true,
			},
			hasCreds: true,
			wantErr:  "incompatible with trust_proxy",
		},
		{
			name: "public-tailscale ok",
			p: profile{
				Mode: modePublicTailscale, BindHTTP: "0.0.0.0:8443", BindP2P: "0.0.0.0:9000",
				PublicHost: "udisend-host.fluffy-otter.ts.net",
			},
			hasCreds: true,
		},
		{
			name: "public-tailscale with TLS files rejected",
			p: profile{
				Mode: modePublicTailscale, BindHTTP: "0.0.0.0:8443", BindP2P: "0.0.0.0:9000",
				PublicHost: "udisend-host.fluffy-otter.ts.net",
				TLSCert:    "/x/c", TLSKey: "/x/k",
			},
			hasCreds: true,
			wantErr:  "tailscaled manages them",
		},
		{
			name: "public-tailscale with trust_proxy rejected",
			p: profile{
				Mode: modePublicTailscale, BindHTTP: "0.0.0.0:8443", BindP2P: "0.0.0.0:9000",
				PublicHost: "udisend-host.fluffy-otter.ts.net",
				TrustProxy: true,
			},
			hasCreds: true,
			wantErr:  "incompatible with trust_proxy",
		},
		{
			name: "public-proxy ok",
			p: profile{
				Mode: modePublicProxy, BindHTTP: "127.0.0.1:8443", BindP2P: "0.0.0.0:9000",
				PublicHost: "udisend.example.com",
				TrustProxy: true,
			},
			hasCreds: true,
		},
		{
			name: "public-proxy without trust_proxy",
			p: profile{
				Mode: modePublicProxy, BindHTTP: "127.0.0.1:8443", BindP2P: "0.0.0.0:9000",
				PublicHost: "udisend.example.com",
			},
			hasCreds: true,
			wantErr:  "requires trust_proxy",
		},
		{
			name: "public-proxy with TLS forbidden",
			p: profile{
				Mode: modePublicProxy, BindHTTP: "127.0.0.1:8443", BindP2P: "0.0.0.0:9000",
				PublicHost: "udisend.example.com",
				TrustProxy: true,
				TLSCert:    "/x/c", TLSKey: "/x/k",
			},
			hasCreds: true,
			wantErr:  "forbids tls_cert",
		},
		{
			name:    "unknown mode",
			p:       profile{Mode: "weird", BindHTTP: "127.0.0.1:0", BindP2P: "127.0.0.1:0"},
			wantErr: "unknown mode",
		},
		{
			name:    "missing bind_http",
			p:       profile{Mode: modeLoopback, BindP2P: "127.0.0.1:0"},
			wantErr: "bind_http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.p.validate(tt.hasCreds)
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

func TestIsLoopbackBind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:0", true},
		{"127.0.0.1:8443", true},
		{"[::1]:8443", true},
		{"0.0.0.0:8443", false},
		{"[::]:8443", false},
		{":8443", false},
		{"192.168.0.105:8443", false},
		{"udisend.local:8443", false},
		{"garbage", false},
	}
	for _, tt := range tests {
		if got := isLoopbackBind(tt.addr); got != tt.want {
			t.Errorf("isLoopbackBind(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

// TestSplitHostPort pins behaviour across IPv4, IPv6, hostnames, and
// bare-host inputs — guarding against the "fe80::1:8443" round-trip
// trap where naive concatenation produces a different address.
func TestSplitHostPort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in       string
		def      string
		wantHost string
		wantPort string
	}{
		{"192.168.0.105", "8443", "192.168.0.105", "8443"},
		{"192.168.0.105:9000", "8443", "192.168.0.105", "9000"},
		{"udisend.example.com", "443", "udisend.example.com", "443"},
		{"udisend.example.com:8443", "443", "udisend.example.com", "8443"},
		{"[fe80::1]:8443", "443", "fe80::1", "8443"},
		{"fe80::1", "8443", "fe80::1", "8443"},
		{"::1", "8443", "::1", "8443"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			host, port, err := splitHostPort(tt.in, tt.def)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if host != tt.wantHost || port != tt.wantPort {
				t.Errorf("splitHostPort(%q,%q) = (%q,%q), want (%q,%q)",
					tt.in, tt.def, host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestProfilePublicMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode deployMode
		want bool
	}{
		{modeLoopback, false},
		{modeLANIP, true},
		{modePublicAutocert, true},
		{modePublicProxy, true},
		{modePublicTailscale, true},
	}
	for _, c := range cases {
		p := profile{Mode: c.mode}
		if got := p.publicMode(); got != c.want {
			t.Errorf("mode %s publicMode = %v, want %v", c.mode, got, c.want)
		}
	}
}
