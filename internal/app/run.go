package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	"github.com/udisondev/udisend/internal/config"
	"github.com/udisondev/udisend/internal/httpui"
	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/network"
	tspkg "github.com/udisondev/udisend/pkg/tailscale"
)

// RunConfig is the input to Run. CLI flag overrides land here as
// non-empty fields; the persisted profile fills in the rest.
type RunConfig struct {
	StorageDir string

	// Override fields — empty means "no override; use profile / default".
	BindHTTP      string
	BindP2P       string
	PublicHost    string
	TLSCert       string
	TLSKey        string
	TrustProxy    bool
	TrustProxySet bool
	OpenBrowser   bool

	Bootstrap []string

	Logger   *slog.Logger
	LogLevel *slog.LevelVar

	// OpenBrowserFn is invoked on the loopback path to open the URL in
	// a browser. CLI binaries pass the OS-specific helper; tests pass
	// nil to skip the side effect.
	OpenBrowserFn func(url string) error
	// PrintBanner, when non-nil, is called once the stack is up so the
	// CLI can print its operator-facing summary. Tests pass nil.
	PrintBanner func(p *config.Profile, url, idHash, fingerprint, udp string)
}

// Run loads the persisted profile, opens the network/messenger/HTTP
// stack, and supervises run loops via errgroup until ctx is cancelled.
// It is the single function cmd/messenger.runMessenger should call.
func Run(ctx context.Context, cfg RunConfig) error {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	dbPath := filepath.Join(cfg.StorageDir, "messenger.db")
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		return err
	}

	prof, err := config.LoadProfile(ctx, store)
	if err != nil {
		return errors.Join(err, store.Close())
	}
	// Zero-config path: no profile means the user has never run
	// `udisend public enable`. Default to loopback — token-in-URL,
	// no password, no setup ceremony.
	if prof == nil {
		prof = &config.Profile{
			Mode:     config.ModeLoopback,
			BindHTTP: config.LoopbackHost + ":0",
			BindP2P:  config.LoopbackHost + ":0",
		}
	}

	applyOverrides(prof, cfg)

	creds, err := store.GetAuthCredentials(ctx)
	if err != nil {
		return errors.Join(err, store.Close())
	}
	if err := prof.Validate(creds != nil); err != nil {
		return errors.Join(err, store.Close())
	}

	// Honour persisted log.level unless verbose explicitly raised it.
	applyPersistedLogLevel(ctx, store, cfg.LogLevel)

	identityPath := filepath.Join(cfg.StorageDir, "messenger.key")
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
		Bootstrap:              cfg.Bootstrap,
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
		Public:     prof.PublicMode(),
		PublicHost: prof.PublicHost,
		TrustProxy: prof.TrustProxy,
		TLSCert:    prof.TLSCert,
		TLSKey:     prof.TLSKey,
		LogLevel:   cfg.LogLevel,
		DBPath:     dbPath,
		// Self-signed cert deployments cause browsers to flip the document
		// into opaque origin after the cert override, leaving every form
		// POST with `Origin: null`. Tolerate that only in lan-ip mode.
		AllowOpaqueOrigin: prof.Mode == config.ModeLANIP,
	}

	var (
		acmeServer *ACMEServer
		tsRefresh  *TailscaleCertRefresher
	)
	switch prof.Mode {
	case config.ModePublicAutocert:
		acmeDir := filepath.Join(cfg.StorageDir, "autocert")
		acmeServer = NewACMEServer(prof.PublicHost, acmeDir)
		httpCfg.TLSConfig = acmeServer.TLSConfig()
		httpCfg.TLSCert = ""
		httpCfg.TLSKey = ""

	case config.ModePublicTailscale:
		tsClient := tspkg.New()
		if err := tsClient.Detect(ctx); err != nil {
			return errors.Join(fmt.Errorf("tailscale: %w", err), node.Close(), store.Close())
		}
		tsRefresh = NewTailscaleCertRefresher(tsClient, prof.PublicHost, logger)
		// Fail-fast on first cert fetch — if tailscaled or LE has problems,
		// we want the operator to see it now, not on the first browser hit.
		if err := tsRefresh.Fetch(ctx); err != nil {
			return errors.Join(fmt.Errorf("tailscale cert: %w", err), node.Close(), store.Close())
		}
		httpCfg.TLSConfig = tsRefresh.TLSConfig()
		httpCfg.TLSCert = ""
		httpCfg.TLSKey = ""
	}

	srv, err := httpui.NewServer(httpCfg)
	if err != nil {
		mngr.Close()
		return errors.Join(err, node.Close(), store.Close())
	}

	url := srv.URL()
	logger.Info("UI ready", "address", srv.LocalAddress(), "public", prof.PublicMode())
	if cfg.PrintBanner != nil {
		cfg.PrintBanner(prof, url, id.Public().DestinationHash().String(), id.Public().Fingerprint(), node.LocalAddress())
	}

	if cfg.OpenBrowser && !prof.PublicMode() && cfg.OpenBrowserFn != nil {
		if err := cfg.OpenBrowserFn(url); err != nil {
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
			tsRefresh.Run(gctx)
			return nil
		})
	}

	runErr := g.Wait()

	mngr.Close()
	closeErr := errors.Join(node.Close(), store.Close())

	return errors.Join(runErr, closeErr)
}

// applyOverrides copies non-empty CLI overrides onto the persisted
// profile. Empty values mean "do not override".
func applyOverrides(p *config.Profile, ov RunConfig) {
	if ov.BindHTTP != "" {
		p.BindHTTP = ov.BindHTTP
	}
	if ov.BindP2P != "" {
		p.BindP2P = ov.BindP2P
	}
	if ov.PublicHost != "" {
		p.PublicHost = ov.PublicHost
	}
	if ov.TLSCert != "" {
		p.TLSCert = ov.TLSCert
	}
	if ov.TLSKey != "" {
		p.TLSKey = ov.TLSKey
	}
	if ov.TrustProxySet {
		p.TrustProxy = ov.TrustProxy
	}
}

// applyPersistedLogLevel reads log.level from app_settings and bumps
// the supplied LevelVar accordingly. Skipped when LevelVar is nil
// (caller pinned the level via -v) or when no setting is persisted.
func applyPersistedLogLevel(ctx context.Context, store *storage.Store, lv *slog.LevelVar) {
	if lv == nil {
		return
	}
	v, ok, _ := store.GetSetting(ctx, "log.level")
	if !ok {
		return
	}
	switch v {
	case "debug":
		lv.Set(slog.LevelDebug)
	case "warn":
		lv.Set(slog.LevelWarn)
	case "error":
		lv.Set(slog.LevelError)
	}
}
