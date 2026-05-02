// Command network runs a udisend network node — a passive participant
// that joins the DHT, relays signaling traffic for clients, and (when
// configured) serves embedded STUN/TURN volunteers.
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/udisondev/udisend/internal/config"
	"github.com/udisondev/udisend/internal/network"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func run() error {
	defaultIdentity := filepath.Join(defaultStateDir(), "network.key")

	listen := flag.String("listen", "127.0.0.1:9000", "UDP listen address")
	identityPath := flag.String("identity", defaultIdentity, "path to identity seed file")
	publicIP := flag.String("public-ip", "", "advertise STUN/TURN volunteer on this IP (omit to disable)")
	stunAddr := flag.String("stun", ":3478", "STUN listen address (used only if --public-ip is set)")
	turnAddr := flag.String("turn", ":3479", "TURN listen address (used only if --public-ip is set)")
	turnSecret := flag.String("turn-secret", "", "shared secret for TURN long-term credentials (omit to disable TURN)")
	bootstrap := stringList{}
	flag.Var(&bootstrap, "bootstrap", "peer address to bootstrap from (host:port; can be repeated)")
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
	logger.Info("udisend network node",
		"identity", id.Public().DestinationHash().String(),
		"fingerprint", id.Public().Fingerprint())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	node, err := network.Open(ctx, network.Config{
		Identity:   id,
		Listen:     *listen,
		PublicIP:   *publicIP,
		STUNAddr:   *stunAddr,
		TURNAddr:   *turnAddr,
		TURNSecret: *turnSecret,
		Bootstrap:  bootstrap,
		Logger:     logger,
	})
	if err != nil {
		return err
	}
	logger.Info("listening", "udp", node.LocalAddress())
	return node.Run(ctx)
}

func defaultStateDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "udisend")
}
