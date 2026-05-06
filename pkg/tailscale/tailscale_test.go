package tailscale

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	tslocal "tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// fakeRT lets tests stub responses to specific Local API paths without
// running a real tailscaled.
type fakeRT struct {
	handler http.HandlerFunc
}

func (f *fakeRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := &recorder{header: http.Header{}, body: &strings.Builder{}}
	f.handler(rec, r)
	if rec.statusCode == 0 {
		rec.statusCode = http.StatusOK
	}
	return &http.Response{
		StatusCode: rec.statusCode,
		Header:     rec.header,
		Body:       io.NopCloser(strings.NewReader(rec.body.String())),
		Request:    r,
	}, nil
}

type recorder struct {
	header     http.Header
	body       *strings.Builder
	statusCode int
}

func (r *recorder) Header() http.Header       { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(s int)          { r.statusCode = s }

func clientWith(handler http.HandlerFunc) *Client {
	return &Client{lc: &tslocal.Client{Transport: &fakeRT{handler: handler}}}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestDetect_OK(t *testing.T) {
	t.Parallel()
	c := clientWith(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/localapi/v0/status") {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, &ipnstate.Status{
			BackendState: "Running",
			Self:         &ipnstate.PeerStatus{DNSName: "udisend-host.fluffy-otter.ts.net."},
		})
	})
	if err := c.Detect(t.Context()); err != nil {
		t.Fatalf("Detect: unexpected error %v", err)
	}
}

func TestDetect_NotRunning(t *testing.T) {
	t.Parallel()
	c := clientWith(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no daemon", http.StatusServiceUnavailable)
	})
	err := c.Detect(t.Context())
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Detect: want ErrNotRunning, got %v", err)
	}
}

func TestDetect_NotLoggedIn(t *testing.T) {
	t.Parallel()
	c := clientWith(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, &ipnstate.Status{BackendState: "NeedsLogin"})
	})
	err := c.Detect(t.Context())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Detect: want ErrNotLoggedIn, got %v", err)
	}
}

func TestDetect_NoMagicDNS(t *testing.T) {
	t.Parallel()
	c := clientWith(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, &ipnstate.Status{
			BackendState: "Running",
			Self:         &ipnstate.PeerStatus{DNSName: ""},
		})
	})
	err := c.Detect(t.Context())
	if !errors.Is(err, ErrNoMagicDNS) {
		t.Fatalf("Detect: want ErrNoMagicDNS, got %v", err)
	}
}

func TestHostname_StripsTrailingDot(t *testing.T) {
	t.Parallel()
	c := clientWith(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, &ipnstate.Status{
			BackendState: "Running",
			Self:         &ipnstate.PeerStatus{DNSName: "udisend-host.fluffy-otter.ts.net."},
		})
	})
	got, err := c.Hostname(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := "udisend-host.fluffy-otter.ts.net"; got != want {
		t.Errorf("Hostname = %q, want %q (no trailing dot)", got, want)
	}
}

// genCertPEM produces a well-formed PEM-encoded cert+key pair so we
// can assert Cert() returns a parseable *tls.Certificate.
func genCertPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func TestCert_OK(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := genCertPEM(t)

	c := clientWith(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/localapi/v0/cert/") {
			http.NotFound(w, r)
			return
		}
		// LocalAPI's pair format: private key PEM first, then cert PEM.
		// The lib splits on the "--\n--" boundary between the END of the
		// key block and the BEGIN of the cert block.
		_, _ = w.Write(keyPEM)
		_, _ = w.Write(certPEM)
	})

	cert, err := c.Cert(t.Context(), "udisend-host.fluffy-otter.ts.net")
	if err != nil {
		t.Fatalf("Cert: %v", err)
	}
	if cert == nil || cert.Certificate == nil {
		t.Fatal("Cert returned nil cert")
	}
}

func TestCert_HTTPSDisabled(t *testing.T) {
	t.Parallel()
	c := clientWith(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, "Tailscale HTTPS certs are not enabled for this tailnet")
	})
	_, err := c.Cert(t.Context(), "host.tail-net.ts.net")
	if err == nil {
		t.Fatal("Cert: want error, got nil")
	}
}

// silence unused import lint when test compiled without exercising it.
var _ = tailcfg.CurrentCapabilityVersion
