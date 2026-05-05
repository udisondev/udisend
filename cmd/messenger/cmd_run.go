package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/udisondev/udisend/internal/config"
	"github.com/udisondev/udisend/internal/httpui"
	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/network"
	tspkg "github.com/udisondev/udisend/pkg/tailscale"
)

// newRunCommand builds the `udisend run` cobra subcommand. Without flags
// it reads the persisted profile and starts the server. Flags are
// available as overrides for ops.
func newRunCommand() *cobra.Command {
	var (
		bindHTTP    string
		bindP2P     string
		publicHost  string
		tlsCert     string
		tlsKey      string
		trustProxy  bool
		openBrowser bool
		verbose     bool
		bootstrap   []string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start the messenger (loopback by default)",
		Long: `Start the messenger using the persisted deployment profile.

With no profile (i.e. you've never run "udisend public enable"), defaults
to a loopback bind that prints a token URL — no passphrase, no setup.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			storageDir, _ := cmd.Flags().GetString("storage")
			overrides := runOverrides{
				bindHTTP:   bindHTTP,
				bindP2P:    bindP2P,
				publicHost: publicHost,
				tlsCert:    tlsCert,
				tlsKey:     tlsKey,
				trustProxy: trustProxy,
				// Detect whether the user explicitly passed --trust-proxy so
				// the override can flip the bit in either direction. cobra
				// surfaces this via Flags().Changed.
				trustProxySet: cmd.Flags().Changed("trust-proxy"),
				openBrowser:   openBrowser,
				verbose:       verbose,
				bootstrap:     bootstrap,
			}

			return runMessenger(storageDir, overrides)
		},
	}
	cmd.Flags().StringVar(&bindHTTP, "bind-http", "", "override HTTP bind address (e.g. 0.0.0.0:8443)")
	cmd.Flags().StringVar(&bindP2P, "bind-p2p", "", "override P2P UDP bind address")
	cmd.Flags().StringVar(&publicHost, "public-host", "", "override externally-visible hostname")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "override TLS cert path")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "override TLS key path")
	cmd.Flags().BoolVar(&trustProxy, "trust-proxy", false, "honour X-Forwarded-* (override profile)")
	cmd.Flags().BoolVar(&openBrowser, "open", true, "open the browser UI on start (loopback only)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose logs")
	cmd.Flags().StringArrayVar(&bootstrap, "bootstrap", nil, "additional bootstrap peer (host:port; can be repeated)")

	return cmd
}

type runOverrides struct {
	bindHTTP, bindP2P, publicHost, tlsCert, tlsKey string
	trustProxy                                     bool
	trustProxySet                                  bool
	openBrowser                                    bool
	verbose                                        bool
	bootstrap                                      []string
}

// runMessenger does the actual work: load profile, apply overrides, open
// stack, supervise the run loops via errgroup.
func runMessenger(storageDir string, ov runOverrides) error {
	logLevel := new(slog.LevelVar)
	if ov.verbose {
		logLevel.Set(slog.LevelDebug)
	} else {
		logLevel.Set(slog.LevelInfo)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(storageDir, 0o700); err != nil {
		return fmt.Errorf("storage dir: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dbPath := filepath.Join(storageDir, "messenger.db")
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		return err
	}

	prof, err := loadProfile(ctx, store)
	if err != nil {
		return errors.Join(err, store.Close())
	}
	// Zero-config path: no profile means the user has never run
	// `udisend public enable`. Default to loopback — token-in-URL,
	// no password, no setup ceremony.
	if prof == nil {
		prof = &profile{
			Mode:     modeLoopback,
			BindHTTP: loopbackHost + ":0",
			BindP2P:  loopbackHost + ":0",
		}
	}

	if ov.bindHTTP != "" {
		prof.BindHTTP = ov.bindHTTP
	}
	if ov.bindP2P != "" {
		prof.BindP2P = ov.bindP2P
	}
	if ov.publicHost != "" {
		prof.PublicHost = ov.publicHost
	}
	if ov.tlsCert != "" {
		prof.TLSCert = ov.tlsCert
	}
	if ov.tlsKey != "" {
		prof.TLSKey = ov.tlsKey
	}
	if ov.trustProxySet {
		prof.TrustProxy = ov.trustProxy
	}

	creds, err := store.GetAuthCredentials(ctx)
	if err != nil {
		return errors.Join(err, store.Close())
	}
	if err := prof.validate(creds != nil); err != nil {
		return errors.Join(err, store.Close())
	}

	// Honour persisted log.level unless -v explicitly raised it.
	if !ov.verbose {
		if v, ok, _ := store.GetSetting(ctx, "log.level"); ok {
			switch v {
			case "debug":
				logLevel.Set(slog.LevelDebug)
			case "warn":
				logLevel.Set(slog.LevelWarn)
			case "error":
				logLevel.Set(slog.LevelError)
			}
		}
	}

	identityPath := filepath.Join(storageDir, "messenger.key")
	id, err := config.LoadOrCreateIdentity(identityPath)
	if err != nil {
		return errors.Join(err, store.Close())
	}
	logger.Info("udisend messenger",
		"identity", id.Public().DestinationHash().String(),
		"fingerprint", id.Public().Fingerprint(),
		"mode", prof.Mode)

	node, err := network.Open(ctx, network.Config{
		Identity:               id,
		Mode:                   network.ModeClient,
		Listen:                 prof.BindP2P,
		Bootstrap:              ov.bootstrap,
		Logger:                 logger,
		SeenPeerStore:          store,
		BootstrapOverrideStore: store,
	})
	if err != nil {
		return errors.Join(err, store.Close())
	}
	logger.Info("listening", "udp", node.LocalAddress())

	mngr := messenger.Open(messenger.Config{
		Network: node,
		Storage: store,
		Logger:  logger,
	})

	httpCfg := httpui.Config{
		Messenger:  mngr,
		Listen:     prof.BindHTTP,
		Logger:     logger,
		Public:     prof.publicMode(),
		PublicHost: prof.PublicHost,
		TrustProxy: prof.TrustProxy,
		TLSCert:    prof.TLSCert,
		TLSKey:     prof.TLSKey,
		LogLevel:   logLevel,
		DBPath:     dbPath,
		// Self-signed cert deployments cause browsers to flip the document
		// into opaque origin after the cert override, leaving every form
		// POST with `Origin: null`. Tolerate that only in lan-ip mode.
		AllowOpaqueOrigin: prof.Mode == modeLANIP,
	}

	// Mode-specific TLS plumbing.
	var (
		acmeServer *acmeChallengeServer
		tsRefresh  *tailscaleCertRefresher
	)
	switch prof.Mode {
	case modePublicAutocert:
		acmeDir := filepath.Join(storageDir, "autocert")
		acmeServer = newACME(prof.PublicHost, acmeDir)
		httpCfg.TLSConfig = acmeServer.TLSConfig()
		httpCfg.TLSCert = ""
		httpCfg.TLSKey = ""

	case modePublicTailscale:
		tsClient := tspkg.New()
		if err := tsClient.Detect(ctx); err != nil {
			return errors.Join(fmt.Errorf("tailscale: %w", err), node.Close(), store.Close())
		}
		tsRefresh = newTailscaleCertRefresher(tsClient, prof.PublicHost, logger)
		// Fail-fast on first cert fetch — if tailscaled or LE has problems,
		// we want the operator to see it now, not on the first browser hit.
		if err := tsRefresh.fetch(ctx); err != nil {
			return errors.Join(fmt.Errorf("tailscale cert: %w", err), node.Close(), store.Close())
		}
		httpCfg.TLSConfig = tsRefresh.tlsConfig()
		httpCfg.TLSCert = ""
		httpCfg.TLSKey = ""
	}

	srv, err := httpui.NewServer(httpCfg)
	if err != nil {
		mngr.Close()
		return errors.Join(err, node.Close(), store.Close())
	}

	url := srv.URL()
	logger.Info("UI ready", "address", srv.LocalAddress(), "public", prof.publicMode())
	printRunBanner(prof, url, id.Public().DestinationHash().String(), id.Public().Fingerprint(), node.LocalAddress())

	if ov.openBrowser && !prof.publicMode() {
		if err := openInBrowser(url); err != nil {
			logger.Warn("open browser failed (open the URL above manually)", "err", err)
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := node.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("network: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		if err := mngr.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("messenger: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		if err := srv.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("httpui: %w", err)
		}
		return nil
	})
	if acmeServer != nil {
		g.Go(func() error {
			if err := acmeServer.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("acme: %w", err)
			}
			return nil
		})
	}
	if tsRefresh != nil {
		g.Go(func() error {
			tsRefresh.run(gctx)
			return nil
		})
	}

	runErr := g.Wait()

	mngr.Close()
	closeErr := errors.Join(node.Close(), store.Close())

	return errors.Join(runErr, closeErr)
}

// tailscaleCertRefresher fetches and periodically refreshes a TLS
// certificate from tailscaled. Tailscale's daemon caches and renews the
// LE-issued cert under the hood; we just have to call it again every so
// often to pick up fresh material.
type tailscaleCertRefresher struct {
	client *tspkg.Client
	host   string
	logger *slog.Logger
	cert   atomic.Pointer[tls.Certificate]
}

func newTailscaleCertRefresher(c *tspkg.Client, host string, logger *slog.Logger) *tailscaleCertRefresher {
	return &tailscaleCertRefresher{client: c, host: host, logger: logger}
}

// fetch synchronously requests the cert and stashes it. Used for the
// startup fail-fast and for periodic refresh.
func (t *tailscaleCertRefresher) fetch(ctx context.Context) error {
	cert, err := t.client.CertWithDeadline(ctx, t.host, 90*time.Second)
	if err != nil {
		return err
	}
	t.cert.Store(cert)
	return nil
}

// tlsConfig returns a *tls.Config whose GetCertificate reads from the
// atomic pointer. Old connections holding a previous Certificate keep
// working; new handshakes pick up the freshest material.
func (t *tailscaleCertRefresher) tlsConfig() *tls.Config {
	return &tls.Config{
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			c := t.cert.Load()
			if c == nil {
				return nil, errors.New("tailscale cert not available yet")
			}
			return c, nil
		},
	}
}

// run loops every 12h calling fetch until ctx is cancelled. LE certs
// are 90 days; 12h is small enough that the tailscaled-side rotation
// (which happens at ~T-30d) is reflected within half a day.
func (t *tailscaleCertRefresher) run(ctx context.Context) {
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := t.fetch(ctx); err != nil {
				t.logger.Warn("tailscale: cert refresh failed (will retry next tick)", "err", err)
			}
		}
	}
}

func printRunBanner(p *profile, url, idHash, fingerprint, udp string) {
	fmt.Println()
	fmt.Println("┌─ udisend messenger ──────────────────────────────────────────────")
	switch p.Mode {
	case modeLoopback:
		fmt.Println("│  Open this URL in your browser (auth token included):")
	case modePublicTailscale:
		fmt.Println("│  Public mode (Tailscale) — sign in with your passphrase:")
	default:
		fmt.Println("│  Public mode — sign in with your passphrase:")
	}
	fmt.Println("│  ", url)
	fmt.Println("│")
	fmt.Println("│  My destination_hash:", idHash)
	fmt.Println("│  My fingerprint:     ", fingerprint)
	fmt.Println("│  My UDP address:     ", udp)

	switch p.Mode {
	case modeLANIP, modePublicAutocert, modePublicProxy:
		if ips := localIPsForBanner(); len(ips) > 0 {
			fmt.Println("│")
			fmt.Println("│  Reachable on this host's IPs:")
			for _, ip := range ips {
				fmt.Println("│    -", ip)
			}
		}
	case modePublicTailscale:
		fmt.Println("│")
		fmt.Println("│  Reachable from any device on your tailnet (with Tailscale installed).")
	}

	fmt.Println("└──────────────────────────────────────────────────────────────────")
	fmt.Println()
}

// localIPsForBanner enumerates every up, non-loopback IPv4 address.
func localIPsForBanner() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil {
				continue
			}
			out = append(out, fmt.Sprintf("%s (%s)", ip.String(), ifc.Name))
		}
	}

	return out
}

func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
