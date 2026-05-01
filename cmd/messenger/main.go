// Command messenger runs the udisend messenger client (fyne desktop GUI).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/udisondev/udisend/internal/config"
	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/ui"
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
	listen := flag.String("listen", "127.0.0.1:0", "UDP listen address")
	bootstrap := stringList{}
	flag.Var(&bootstrap, "bootstrap", "network-node address (host:port; can be repeated)")
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
		Listen:     *listen,
		StorageDir: *storageDir,
		Logger:     logger,
	})
	if err != nil {
		return err
	}
	defer mngr.Close()

	logger.Info("listening", "udp", mngr.LocalAddress())

	go mngr.Run(ctx)
	return ui.Run(ctx, mngr)
}

func defaultStateDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "udisend", "messenger")
}
