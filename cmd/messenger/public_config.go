package main

import (
	"errors"
	"net"
)

// publicConfig collects the flags that control how the webui exposes
// itself when bound to a non-loopback address.
type publicConfig struct {
	PublicHost string
	TrustProxy bool
	TLSCert    string
	TLSKey     string
}

// resolvePublicMode decides whether the server should run in public mode
// based on the listen address, and validates that the operator supplied
// the prerequisites. Returns (true, nil) for public mode, (false, nil)
// for loopback (legacy), or (_, err) when the operator's combination of
// flags is unsafe and we should fail closed.
//
// Conditions checked (only when bind is non-loopback):
//   - hasCreds: passphrase has been set via -set-password.
//   - TLS plumbing: either -tls-cert+-tls-key are both supplied OR
//     -trust-proxy is set (caller asserts an upstream TLS terminator).
func resolvePublicMode(listenAddr string, hasCreds bool, cfg publicConfig) (bool, error) {
	if isLoopbackBind(listenAddr) {
		return false, nil
	}
	if !hasCreds {
		return false, errors.New(
			"non-loopback HTTP bind requires an existing passphrase. " +
				"Run `messenger -set-password` first")
	}
	hasCert := cfg.TLSCert != "" && cfg.TLSKey != ""
	mixedCert := (cfg.TLSCert != "") != (cfg.TLSKey != "")
	if mixedCert {
		return false, errors.New("-tls-cert and -tls-key must be supplied together")
	}
	if !hasCert && !cfg.TrustProxy {
		return false, errors.New(
			"non-loopback HTTP bind requires TLS. " +
				"Either pass -tls-cert <cert.pem> -tls-key <key.pem>, " +
				"or run a TLS-terminating reverse proxy (e.g. Caddy) and pass -trust-proxy")
	}
	if cfg.PublicHost == "" {
		return false, errors.New(
			"non-loopback HTTP bind requires -public-host (the externally-visible " +
				"hostname, e.g. messenger.example.com or 1-2-3-4.sslip.io). " +
				"This is the URL printed in the startup banner and used for " +
				"same-origin checks on /login")
	}

	return true, nil
}

// isLoopbackBind interprets a Listen string. Loopback IPs (127.0.0.0/8,
// ::1) are treated as loopback. The empty host (`:9000`), hostnames, and
// `0.0.0.0`/`::` are treated as non-loopback. Returns false on unparseable
// input — the caller should also surface bind errors.
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	return ip.IsLoopback()
}

