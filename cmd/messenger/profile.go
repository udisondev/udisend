package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/udisondev/udisend/internal/storage"
)

// deployMode is the persisted deployment profile chosen via
// `udisend public -enable`. It dictates how the webui is bound and
// authenticated.
type deployMode string

const (
	modeLoopback        deployMode = "loopback"
	modeLANIP           deployMode = "lan-ip"
	modePublicAutocert  deployMode = "public-autocert"
	modePublicProxy     deployMode = "public-proxy"
	modePublicTailscale deployMode = "public-tailscale"
)

// profile is the in-memory view of the persisted deployment configuration.
// Only `Mode` is required; the rest are mode-dependent and validated by
// `validate`. `BindHTTP` and `BindP2P` always have defaults the CLI fills in
// during init.
type profile struct {
	Mode       deployMode
	BindHTTP   string // e.g. "127.0.0.1:0", "0.0.0.0:8443"
	BindP2P    string // e.g. "127.0.0.1:0", "0.0.0.0:9000"
	PublicHost string // e.g. "192.168.0.105:8443" or "udisend.example.com"
	TLSCert    string // path; empty for loopback / public-proxy
	TLSKey     string // path; empty for loopback / public-proxy
	TrustProxy bool   // only true in public-proxy mode
}

const (
	settingDeployMode       = "deploy.mode"
	settingDeployBindHTTP   = "deploy.bind_http"
	settingDeployBindP2P    = "deploy.bind_p2p"
	settingDeployPublicHost = "deploy.public_host"
	settingDeployTLSCert    = "deploy.tls_cert"
	settingDeployTLSKey     = "deploy.tls_key"
	settingDeployTrustProxy = "deploy.trust_proxy"

	loopbackHost    = "127.0.0.1"
	defaultHTTPPort = "8443"
	defaultP2PPort  = "9000"
)

// loadProfile reads the persisted profile from app_settings. Returns
// (nil, nil) if `udisend public -enable` has never been run (Mode key absent).
func loadProfile(ctx context.Context, store *storage.Store) (*profile, error) {
	mode, ok, err := store.GetSetting(ctx, settingDeployMode)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	get := func(key string) (string, error) {
		v, _, err := store.GetSetting(ctx, key)
		return v, err
	}
	bindHTTP, err := get(settingDeployBindHTTP)
	if err != nil {
		return nil, err
	}
	bindP2P, err := get(settingDeployBindP2P)
	if err != nil {
		return nil, err
	}
	publicHost, err := get(settingDeployPublicHost)
	if err != nil {
		return nil, err
	}
	tlsCert, err := get(settingDeployTLSCert)
	if err != nil {
		return nil, err
	}
	tlsKey, err := get(settingDeployTLSKey)
	if err != nil {
		return nil, err
	}
	trustProxyStr, err := get(settingDeployTrustProxy)
	if err != nil {
		return nil, err
	}
	trustProxy, _ := strconv.ParseBool(trustProxyStr)

	return &profile{
		Mode:       deployMode(mode),
		BindHTTP:   bindHTTP,
		BindP2P:    bindP2P,
		PublicHost: publicHost,
		TLSCert:    tlsCert,
		TLSKey:     tlsKey,
		TrustProxy: trustProxy,
	}, nil
}

// saveProfile upserts every field. Caller is expected to have called
// validate first.
func saveProfile(ctx context.Context, store *storage.Store, p *profile) error {
	pairs := []struct{ key, val string }{
		{settingDeployMode, string(p.Mode)},
		{settingDeployBindHTTP, p.BindHTTP},
		{settingDeployBindP2P, p.BindP2P},
		{settingDeployPublicHost, p.PublicHost},
		{settingDeployTLSCert, p.TLSCert},
		{settingDeployTLSKey, p.TLSKey},
		{settingDeployTrustProxy, strconv.FormatBool(p.TrustProxy)},
	}
	for _, kv := range pairs {
		if err := store.SetSetting(ctx, kv.key, kv.val); err != nil {
			return fmt.Errorf("save %s: %w", kv.key, err)
		}
	}

	return nil
}

// validate enforces the cross-field invariants that resolvePublicMode used
// to enforce. Each mode has its own required-vs-forbidden field shape.
func (p *profile) validate(hasCreds bool) error {
	if p.BindHTTP == "" {
		return errors.New("profile: bind_http is empty")
	}
	if p.BindP2P == "" {
		return errors.New("profile: bind_p2p is empty")
	}

	switch p.Mode {
	case modeLoopback:
		if !isLoopbackBind(p.BindHTTP) {
			return fmt.Errorf("profile: loopback mode requires loopback bind_http, got %q", p.BindHTTP)
		}

	case modeLANIP, modePublicAutocert, modePublicProxy, modePublicTailscale:
		if !hasCreds {
			return errors.New("profile: non-loopback mode requires a passphrase (run `udisend public -enable`)")
		}
		if p.PublicHost == "" {
			return errors.New("profile: non-loopback mode requires public_host")
		}
		switch p.Mode {
		case modeLANIP:
			// Self-signed cert files persisted alongside the DB.
			if p.TLSCert == "" || p.TLSKey == "" {
				return errors.New("profile: lan-ip mode requires tls_cert and tls_key")
			}
			if p.TrustProxy {
				return errors.New("profile: lan-ip mode is incompatible with trust_proxy")
			}
		case modePublicAutocert:
			// autocert manages cert lifecycle; user must not supply files.
			if p.TLSCert != "" || p.TLSKey != "" {
				return errors.New("profile: public-autocert mode forbids tls_cert/tls_key (autocert manages them)")
			}
			if p.TrustProxy {
				return errors.New("profile: public-autocert mode is incompatible with trust_proxy")
			}
		case modePublicProxy:
			if !p.TrustProxy {
				return errors.New("profile: public-proxy mode requires trust_proxy=true")
			}
			if p.TLSCert != "" || p.TLSKey != "" {
				return errors.New("profile: public-proxy mode forbids tls_cert/tls_key (proxy terminates TLS)")
			}
		case modePublicTailscale:
			// Tailscale issues + auto-renews the cert via its ACME pipeline;
			// the operator must not supply files. TrustProxy doesn't make
			// sense either — the daemon's TLS terminates in-process.
			if p.TLSCert != "" || p.TLSKey != "" {
				return errors.New("profile: public-tailscale mode forbids tls_cert/tls_key (tailscaled manages them)")
			}
			if p.TrustProxy {
				return errors.New("profile: public-tailscale mode is incompatible with trust_proxy")
			}
		}

	default:
		return fmt.Errorf("profile: unknown mode %q", p.Mode)
	}

	return nil
}

// publicMode returns true when the server should run in public auth mode
// (session cookies, login form). False = loopback (token-in-URL).
func (p *profile) publicMode() bool {
	return p.Mode != modeLoopback
}

// isLoopbackBind reports whether host:port binds to a loopback interface.
// Empty host, hostnames, and 0.0.0.0/:: are treated as non-loopback.
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	return ip.IsLoopback()
}

// defaultStorageDir returns the XDG-compliant config directory for udisend.
// On Linux: $XDG_CONFIG_HOME or ~/.config; macOS: ~/Library/Application Support;
// Windows: %AppData%. Falls back to "." if the user has no home directory.
func defaultStorageDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "udisend", "messenger")
}
