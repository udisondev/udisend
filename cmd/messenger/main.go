// Command messenger runs the udisend messenger client. It boots the
// in-process Go runtime (identity, DHT, presence, signaling, storage)
// and a localhost HTTP server that serves the browser-side UI. WebRTC
// itself runs in the browser — point any modern browser at the URL
// printed on stdout (it carries an auth token).
package main

import (
	"context"
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

	"github.com/udisondev/udisend/internal/config"
	"github.com/udisondev/udisend/internal/httpui"
	"github.com/udisondev/udisend/internal/messenger"
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
	flag.Var(&bootstrap, "bootstrap", "peer address to bootstrap from (host:port; can be repeated)")
	openBrowser := flag.Bool("open", true, "open the browser UI on start")
	verbose := flag.Bool("v", false, "verbose logs")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	id, err := config.LoadOrCreateIdentity(*identityPath)
	if err != nil {
		return err
	}
	logger.Info("udisend messenger",
		"identity", id.Public().DestinationHash().String(),
		"fingerprint", id.Public().Fingerprint())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	mngr, err := messenger.Open(ctx, messenger.Config{
		Identity:   id,
		Bootstrap:  bootstrap,
		Listen:     *listenUDP,
		StorageDir: *storageDir,
		Logger:     logger,
	})
	if err != nil {
		return err
	}
	defer mngr.Close()
	logger.Info("listening", "udp", mngr.LocalAddress())

	srv, err := httpui.NewServer(httpui.Config{
		Messenger: mngr,
		Listen:    *listenHTTP,
		Logger:    logger,
	})
	if err != nil {
		return err
	}
	url := srv.URL()
	logger.Info("UI ready", "url", url)
	fmt.Println()
	fmt.Println("┌─ udisend messenger ──────────────────────────────────────────────")
	fmt.Println("│  Open this URL in your browser (auth token included):")
	fmt.Println("│  ", url)
	fmt.Println("│")
	fmt.Println("│  My destination_hash:", id.Public().DestinationHash().String())
	fmt.Println("│  My fingerprint:     ", id.Public().Fingerprint())
	fmt.Println("│  My UDP address:     ", mngr.LocalAddress())
	fmt.Println("└──────────────────────────────────────────────────────────────────")
	fmt.Println()

	if *openBrowser {
		if err := openInBrowser(url); err != nil {
			logger.Warn("open browser failed (open the URL above manually)", "err", err)
		}
	}

	go mngr.Run(ctx)
	return srv.Run(ctx)
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
