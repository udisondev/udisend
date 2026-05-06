package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// generateSelfSignedCert writes a fresh RSA-2048 self-signed cert and key
// to certPath/keyPath. SAN fields are derived from `host` — IP-literals
// land in IPAddresses, everything else in DNSNames. The cert is valid
// for 365 days, which matches what most browsers stop nagging about for
// internal-use deployments.
func generateSelfSignedCert(host, certPath, keyPath string) error {
	if host == "" {
		return fmt.Errorf("cert: host required")
	}

	// Strip optional ":port" — SAN entries do not carry ports.
	hostOnly, _, err := net.SplitHostPort(host)
	if err != nil {
		hostOnly = host
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("cert: gen key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("cert: gen serial: %w", err)
	}

	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostOnly},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	if ip := net.ParseIP(hostOnly); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{hostOnly}
		// "localhost" is conventionally added so curl --resolve scenarios
		// and some browsers' fallback paths still work against this cert.
		if !strings.EqualFold(hostOnly, "localhost") {
			tpl.DNSNames = append(tpl.DNSNames, "localhost")
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("cert: create: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return fmt.Errorf("cert: mkdir: %w", err)
	}

	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("cert: open cert: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		_ = certOut.Close()
		return fmt.Errorf("cert: encode cert: %w", err)
	}
	if err := certOut.Close(); err != nil {
		return fmt.Errorf("cert: close cert: %w", err)
	}

	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("cert: open key: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		_ = keyOut.Close()
		return fmt.Errorf("cert: marshal key: %w", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
		_ = keyOut.Close()
		return fmt.Errorf("cert: encode key: %w", err)
	}
	if err := keyOut.Close(); err != nil {
		return fmt.Errorf("cert: close key: %w", err)
	}

	return nil
}

// readCertExpiry parses the cert PEM and returns the NotAfter timestamp.
// Used by `udisend status`.
func readCertExpiry(certPath string) (time.Time, error) {
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return time.Time{}, fmt.Errorf("cert: no PEM block in %s", certPath)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}

	return c.NotAfter, nil
}
