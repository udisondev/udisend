package config

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

// DeployMode is the persisted deployment profile chosen via the
// `udisend public enable` command. It dictates how the webui is bound
// and authenticated.
type DeployMode string

// Supported deployment modes.
const (
	ModeLoopback        DeployMode = "loopback"
	ModeLANIP           DeployMode = "lan-ip"
	ModePublicAutocert  DeployMode = "public-autocert"
	ModePublicProxy     DeployMode = "public-proxy"
	ModePublicTailscale DeployMode = "public-tailscale"
)

// Profile is the in-memory view of the persisted deployment configuration.
// Only `Mode` is required; the rest are mode-dependent and validated by
// `Validate`. `BindHTTP` and `BindP2P` always have defaults the CLI fills
// in during init.
type Profile struct {
	Mode       DeployMode
	BindHTTP   string // e.g. "127.0.0.1:0", "0.0.0.0:8443"
	BindP2P    string // e.g. "127.0.0.1:0", "0.0.0.0:9000"
	PublicHost string // e.g. "192.168.0.105:8443" or "udisend.example.com"
	TLSCert    string // path; empty for loopback / public-proxy
	TLSKey     string // path; empty for loopback / public-proxy
	TrustProxy bool   // only true in public-proxy mode
}

// Setting keys persisted in app_settings. Exported so callers (CLI
// resets, debugging tools) can clear them.
const (
	SettingDeployMode       = "deploy.mode"
	SettingDeployBindHTTP   = "deploy.bind_http"
	SettingDeployBindP2P    = "deploy.bind_p2p"
	SettingDeployPublicHost = "deploy.public_host"
	SettingDeployTLSCert    = "deploy.tls_cert"
	SettingDeployTLSKey     = "deploy.tls_key"
	SettingDeployTrustProxy = "deploy.trust_proxy"

	// LoopbackHost is the IPv4 loopback address used for default binds.
	LoopbackHost = "127.0.0.1"
	// DefaultHTTPPort is the default port when the operator does not
	// override BindHTTP.
	DefaultHTTPPort = "8443"
	// DefaultP2PPort is the default UDP port when the operator does not
	// override BindP2P.
	DefaultP2PPort = "9000"
)

// LoadProfile reads the persisted profile from app_settings. Returns
// (nil, nil) if `udisend public enable` has never been run (Mode key
// absent).
func LoadProfile(ctx context.Context, store *storage.Store) (*Profile, error) {
	mode, ok, err := store.GetSetting(ctx, SettingDeployMode)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	p := &Profile{Mode: DeployMode(mode)}
	fields := []struct {
		key string
		dst *string
	}{
		{SettingDeployBindHTTP, &p.BindHTTP},
		{SettingDeployBindP2P, &p.BindP2P},
		{SettingDeployPublicHost, &p.PublicHost},
		{SettingDeployTLSCert, &p.TLSCert},
		{SettingDeployTLSKey, &p.TLSKey},
	}
	for _, f := range fields {
		v, _, err := store.GetSetting(ctx, f.key)
		if err != nil {
			return nil, err
		}
		*f.dst = v
	}

	trustProxyStr, _, err := store.GetSetting(ctx, SettingDeployTrustProxy)
	if err != nil {
		return nil, err
	}
	p.TrustProxy, _ = strconv.ParseBool(trustProxyStr)

	return p, nil
}

// SaveProfile upserts every field. Caller is expected to have called
// Validate first.
func SaveProfile(ctx context.Context, store *storage.Store, p *Profile) error {
	pairs := []struct{ key, val string }{
		{SettingDeployMode, string(p.Mode)},
		{SettingDeployBindHTTP, p.BindHTTP},
		{SettingDeployBindP2P, p.BindP2P},
		{SettingDeployPublicHost, p.PublicHost},
		{SettingDeployTLSCert, p.TLSCert},
		{SettingDeployTLSKey, p.TLSKey},
		{SettingDeployTrustProxy, strconv.FormatBool(p.TrustProxy)},
	}
	for _, kv := range pairs {
		if err := store.SetSetting(ctx, kv.key, kv.val); err != nil {
			return fmt.Errorf("save %s: %w", kv.key, err)
		}
	}

	return nil
}

// Validate enforces cross-field invariants. Each mode has its own
// required-vs-forbidden field shape.
func (p *Profile) Validate(hasCreds bool) error {
	if p.BindHTTP == "" {
		return errors.New("profile: bind_http is empty")
	}
	if p.BindP2P == "" {
		return errors.New("profile: bind_p2p is empty")
	}

	switch p.Mode {
	case ModeLoopback:
		return p.validateLoopback()
	case ModeLANIP, ModePublicAutocert, ModePublicProxy, ModePublicTailscale:
		return p.validateNonLoopback(hasCreds)
	default:
		return fmt.Errorf("profile: unknown mode %q", p.Mode)
	}
}

func (p *Profile) validateLoopback() error {
	if !IsLoopbackBind(p.BindHTTP) {
		return fmt.Errorf("profile: loopback mode requires loopback bind_http, got %q", p.BindHTTP)
	}

	return nil
}

func (p *Profile) validateNonLoopback(hasCreds bool) error {
	if !hasCreds {
		return errors.New("profile: non-loopback mode requires a passphrase (run `udisend public enable`)")
	}
	if p.PublicHost == "" {
		return errors.New("profile: non-loopback mode requires public_host")
	}

	switch p.Mode {
	case ModeLANIP:
		return p.validateLANIP()
	case ModePublicAutocert:
		return p.validatePublicAutocert()
	case ModePublicProxy:
		return p.validatePublicProxy()
	case ModePublicTailscale:
		return p.validatePublicTailscale()
	}

	return nil
}

func (p *Profile) validateLANIP() error {
	if p.TLSCert == "" || p.TLSKey == "" {
		return errors.New("profile: lan-ip mode requires tls_cert and tls_key")
	}
	if p.TrustProxy {
		return errors.New("profile: lan-ip mode is incompatible with trust_proxy")
	}

	return nil
}

func (p *Profile) validatePublicAutocert() error {
	if p.TLSCert != "" || p.TLSKey != "" {
		return errors.New("profile: public-autocert mode forbids tls_cert/tls_key (autocert manages them)")
	}
	if p.TrustProxy {
		return errors.New("profile: public-autocert mode is incompatible with trust_proxy")
	}

	return nil
}

func (p *Profile) validatePublicProxy() error {
	if !p.TrustProxy {
		return errors.New("profile: public-proxy mode requires trust_proxy=true")
	}
	if p.TLSCert != "" || p.TLSKey != "" {
		return errors.New("profile: public-proxy mode forbids tls_cert/tls_key (proxy terminates TLS)")
	}

	return nil
}

func (p *Profile) validatePublicTailscale() error {
	if p.TLSCert != "" || p.TLSKey != "" {
		return errors.New("profile: public-tailscale mode forbids tls_cert/tls_key (tailscaled manages them)")
	}
	if p.TrustProxy {
		return errors.New("profile: public-tailscale mode is incompatible with trust_proxy")
	}

	return nil
}

// PublicMode returns true when the server should run in public auth
// mode (session cookies, login form). False = loopback (token-in-URL).
func (p *Profile) PublicMode() bool {
	return p.Mode != ModeLoopback
}

// IsLoopbackBind reports whether host:port binds to a loopback
// interface. Empty host, hostnames, and 0.0.0.0/:: are treated as
// non-loopback.
func IsLoopbackBind(addr string) bool {
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

// DefaultStorageDir returns the XDG-compliant config directory for
// udisend. On Linux: $XDG_CONFIG_HOME or ~/.config; macOS: ~/Library/
// Application Support; Windows: %AppData%. Falls back to "." if the
// user has no home directory.
func DefaultStorageDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "udisend", "messenger")
}
