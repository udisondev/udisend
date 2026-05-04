// Command network runs a udisend public node — a passive participant
// in the DHT that relays signaling traffic for other peers and, when
// given a public IP, optionally serves embedded STUN/TURN volunteers.
// It carries no user state (no contacts, no chat history, no browser
// UI) and is intended for VPS / always-on hosts that act as bootstrap
// and rendezvous infrastructure for the network.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/udisondev/udisend/internal/config"
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
	defaultIdentity := filepath.Join(defaultStateDir(), "network.key")

	identityPath := flag.String("identity", defaultIdentity, "path to identity seed file")
	listen := flag.String("listen", ":9000", "UDP listen address (host:port; \":9000\" binds all interfaces)")
	publicIP := flag.String("public-ip", "", "advertise STUN/TURN volunteer on this IP (omit to skip STUN/TURN)")
	stunAddr := flag.String("stun", ":3478", "STUN listen address (used only if --public-ip is set)")
	turnAddr := flag.String("turn", ":3479", "TURN listen address (used only if --public-ip and --turn-secret are set)")
	turnSecret := flag.String("turn-secret", "", "shared secret for TURN long-term credentials (omit to disable TURN)")
	bootstrap := stringList{}
	flag.Var(&bootstrap, "bootstrap", "peer to seed the routing table — IPv4/IPv6/DNS-name with port, e.g. 1.2.3.4:9000 or relay.example.com:9000 (can be repeated; omit to use community defaults)")
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
		Mode:       network.ModeRelay,
		Listen:     *listen,
		Bootstrap:  bootstrap,
		PublicIP:   *publicIP,
		STUNAddr:   *stunAddr,
		TURNAddr:   *turnAddr,
		TURNSecret: *turnSecret,
		Logger:     logger,
	})
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("┌─ udisend network node ───────────────────────────────────────────")
	fmt.Println("│  destination_hash:", id.Public().DestinationHash().String())
	fmt.Println("│  fingerprint:     ", id.Public().Fingerprint())
	fmt.Println("│  UDP:             ", node.LocalAddress())
	if *publicIP != "" {
		fmt.Println("│  public IP:       ", *publicIP)
		fmt.Println("│  STUN:            ", *stunAddr)
		if *turnSecret != "" {
			fmt.Println("│  TURN:            ", *turnAddr)
		}
	}
	fmt.Println("└──────────────────────────────────────────────────────────────────")
	fmt.Println()

	// A pure relay is not a session destination — drain Income so the
	// pump never blocks. Misbehaving peers that do try to open a noise
	// session here just have their frames Released and dropped.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for inc := range node.Income() {
			inc.Release()
		}
	}()

	runErr := node.Run(ctx)
	<-drainDone

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}

	return nil
}

func defaultStateDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}

	return filepath.Join(dir, "udisend")
}
