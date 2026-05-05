package main

import (
	"strings"
	"testing"
)

func TestIsLoopbackBind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:0", true},
		{"127.0.0.1:9000", true},
		{"[::1]:9000", true},
		{"0.0.0.0:9000", false},
		{"[::]:9000", false},
		{":9000", false},
		{"messenger.example.com:9000", false},
		{"203.0.113.10:9000", false},
		{"garbage", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			t.Parallel()
			if got := isLoopbackBind(tt.addr); got != tt.want {
				t.Errorf("isLoopbackBind(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestResolvePublicMode_LoopbackNeedsNothing(t *testing.T) {
	t.Parallel()

	pub, err := resolvePublicMode("127.0.0.1:0", false, publicConfig{})
	if err != nil {
		t.Errorf("loopback should not require any flag; err = %v", err)
	}
	if pub {
		t.Errorf("loopback should not be public")
	}
}

func TestResolvePublicMode_NonLoopbackWithoutCreds(t *testing.T) {
	t.Parallel()

	_, err := resolvePublicMode("0.0.0.0:9000", false, publicConfig{TrustProxy: true})
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("err = %v, want passphrase error", err)
	}
}

func TestResolvePublicMode_NonLoopbackWithoutTLS(t *testing.T) {
	t.Parallel()

	_, err := resolvePublicMode("0.0.0.0:9000", true, publicConfig{})
	if err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Errorf("err = %v, want TLS error", err)
	}
}

func TestResolvePublicMode_MixedTLSFlags(t *testing.T) {
	t.Parallel()

	_, err := resolvePublicMode("0.0.0.0:9000", true, publicConfig{TLSCert: "c.pem"})
	if err == nil || !strings.Contains(err.Error(), "tls-cert and -tls-key must be supplied together") {
		t.Errorf("err = %v, want mixed-cert error", err)
	}
	_, err = resolvePublicMode("0.0.0.0:9000", true, publicConfig{TLSKey: "k.pem"})
	if err == nil || !strings.Contains(err.Error(), "tls-cert and -tls-key must be supplied together") {
		t.Errorf("err = %v, want mixed-cert error", err)
	}
}

func TestResolvePublicMode_OK_TrustProxy(t *testing.T) {
	t.Parallel()

	pub, err := resolvePublicMode("0.0.0.0:9000", true, publicConfig{
		TrustProxy: true,
		PublicHost: "messenger.example.com",
	})
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if !pub {
		t.Errorf("expected public mode")
	}
}

func TestResolvePublicMode_OK_TLSCert(t *testing.T) {
	t.Parallel()

	pub, err := resolvePublicMode("0.0.0.0:9000", true, publicConfig{
		TLSCert:    "c.pem",
		TLSKey:     "k.pem",
		PublicHost: "messenger.example.com",
	})
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if !pub {
		t.Errorf("expected public mode")
	}
}

func TestResolvePublicMode_RequiresPublicHost(t *testing.T) {
	t.Parallel()

	_, err := resolvePublicMode("0.0.0.0:9000", true, publicConfig{TrustProxy: true})
	if err == nil || !strings.Contains(err.Error(), "public-host") {
		t.Errorf("err = %v, want -public-host error", err)
	}
}
