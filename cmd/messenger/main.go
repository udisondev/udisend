// Command messenger runs the udisend messenger client. It boots the
// in-process Go runtime (identity, DHT, presence, signaling, storage)
// and a localhost HTTP server that serves the browser-side UI. WebRTC
// itself runs in the browser — point any modern browser at the URL
// printed on stdout (it carries an auth token).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/udisondev/udisend/internal/config"
	"github.com/udisondev/udisend/internal/httpui"
	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/network"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	defaultDir := defaultStateDir()
	defaultIdentity := filepath.Join(defaultDir, "messenger.key")

	identityPath := flag.String("identity", defaultIdentity, "path to identity seed file")
	storageDir := flag.String("storage", defaultDir, "directory for SQLite DB and downloads")
	listenUDP := flag.String("listen", "127.0.0.1:0", "UDP listen address (DHT/signaling)")
	listenHTTP := flag.String("http", "127.0.0.1:0", "HTTP listen address (browser UI)")
	bootstrap := stringList{}
	flag.Var(&bootstrap, "bootstrap", "peer to bootstrap from — IPv4/IPv6/DNS-name with port, e.g. 1.2.3.4:9000 or relay.example.com:9000 (can be repeated; omit to use community defaults)")
	openBrowser := flag.Bool("open", true, "open the browser UI on start")
	verbose := flag.Bool("v", false, "verbose logs")

	setPassword := flag.Bool("set-password", false, "interactively set the webui passphrase, then exit (required for non-loopback bind)")
	setupTOTP := flag.Bool("setup-totp", false, "enroll a TOTP second factor and print recovery codes, then exit")
	resetTOTP := flag.Bool("reset-totp", false, "remove TOTP enrollment and recovery codes, then exit")

	publicHost := flag.String("public-host", "", "externally-visible hostname for the printed URL in public mode (e.g. messenger.example.com)")
	trustProxy := flag.Bool("trust-proxy", false, "honor X-Forwarded-For and skip in-process TLS — caller asserts a TLS-terminating reverse proxy is in front (e.g. Caddy)")
	tlsCert := flag.String("tls-cert", "", "path to TLS certificate (PEM); requires -tls-key")
	tlsKey := flag.String("tls-key", "", "path to TLS private key (PEM); requires -tls-cert")
	flag.Parse()

	logLevel := new(slog.LevelVar)
	if *verbose {
		logLevel.Set(slog.LevelDebug)
	} else {
		logLevel.Set(slog.LevelInfo)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	if *setPassword || *setupTOTP || *resetTOTP {
		return runAuthSubcommand(*storageDir, *setPassword, *setupTOTP, *resetTOTP)
	}

	id, err := config.LoadOrCreateIdentity(*identityPath)
	if err != nil {
		return err
	}
	logger.Info("udisend messenger",
		"identity", id.Public().DestinationHash().String(),
		"fingerprint", id.Public().Fingerprint())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := os.MkdirAll(*storageDir, 0o700); err != nil {
		return fmt.Errorf("storage dir: %w", err)
	}
	dbPath := filepath.Join(*storageDir, "messenger.db")
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		return err
	}

	// Persisted log level overrides the -v default unless -v was passed
	// explicitly (in which case the user's CLI choice wins).
	if !*verbose {
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

	creds, err := store.GetAuthCredentials(ctx)
	if err != nil {
		return errors.Join(err, store.Close())
	}
	publicMode, err := resolvePublicMode(*listenHTTP, creds != nil, publicConfig{
		PublicHost: *publicHost,
		TrustProxy: *trustProxy,
		TLSCert:    *tlsCert,
		TLSKey:     *tlsKey,
	})
	if err != nil {
		return errors.Join(err, store.Close())
	}

	node, err := network.Open(ctx, network.Config{
		Identity:               id,
		Mode:                   network.ModeClient,
		Listen:                 *listenUDP,
		Bootstrap:              bootstrap,
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

	srv, err := httpui.NewServer(httpui.Config{
		Messenger:  mngr,
		Listen:     *listenHTTP,
		Logger:     logger,
		Public:     publicMode,
		PublicHost: *publicHost,
		TrustProxy: *trustProxy,
		TLSCert:    *tlsCert,
		TLSKey:     *tlsKey,
		LogLevel:   logLevel,
		DBPath:     dbPath,
	})
	if err != nil {
		mngr.Close()

		return errors.Join(err, node.Close(), store.Close())
	}
	url := srv.URL()
	// Never log the auth-token URL — slog handlers feed systemd journal,
	// docker logs, log shippers etc., where the bearer in `?token=…` would
	// be a credential leak. The full URL is printed to stdout below for
	// the operator's eyes; the structured log only carries non-secrets.
	logger.Info("UI ready", "address", srv.LocalAddress(), "public", publicMode)
	fmt.Println()
	fmt.Println("┌─ udisend messenger ──────────────────────────────────────────────")
	if publicMode {
		fmt.Println("│  Public mode — sign in with your passphrase:")
	} else {
		fmt.Println("│  Open this URL in your browser (auth token included):")
	}
	fmt.Println("│  ", url)
	fmt.Println("│")
	fmt.Println("│  My destination_hash:", id.Public().DestinationHash().String())
	fmt.Println("│  My fingerprint:     ", id.Public().Fingerprint())
	fmt.Println("│  My UDP address:     ", node.LocalAddress())
	fmt.Println("└──────────────────────────────────────────────────────────────────")
	fmt.Println()

	// Suppress auto-open in public mode — typically running on a headless
	// VPS where there is no browser to open.
	if *openBrowser && !publicMode {
		if err := openInBrowser(url); err != nil {
			logger.Warn("open browser failed (open the URL above manually)", "err", err)
		}
	}

	// All three loops share gctx — first non-nil error from any of them
	// cancels the rest, then Wait surfaces it. ctx-cancel from SIGINT
	// flows through the same path as a real failure.
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
		// srv.Run already maps http.ErrServerClosed to nil; what we get
		// here is either a real Serve failure or ctx-driven shutdown.
		if err := srv.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("httpui: %w", err)
		}

		return nil
	})

	runErr := g.Wait()

	// Tear-down order: messenger → node → store. Errors from each are
	// joined so the caller sees the full failure surface, not just the
	// first one. Run loops have already returned by the time we get here.
	mngr.Close()
	closeErr := errors.Join(node.Close(), store.Close())

	return errors.Join(runErr, closeErr)
}

func defaultStateDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "udisend", "messenger")
}

// openInBrowser tries to open the URL in the user's default browser.
// Best-effort — failure just means the user has to copy/paste.
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
