package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/udisondev/udisend/internal/app"
	"github.com/udisondev/udisend/internal/config"
)

// newRunCommand builds the `udisend run` cobra subcommand. Without
// flags it reads the persisted profile and starts the server. Flags
// are available as overrides for ops.
func newRunCommand() *cobra.Command {
	var ov runOverrides
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start the messenger (loopback by default)",
		Long: `Start the messenger using the persisted deployment profile.

With no profile (i.e. you've never run "udisend public enable"), defaults
to a loopback bind that prints a token URL — no passphrase, no setup.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			storageDir, _ := cmd.Flags().GetString("storage")
			ov.trustProxySet = cmd.Flags().Changed("trust-proxy")

			return runMessenger(storageDir, ov)
		},
	}
	cmd.Flags().StringVar(&ov.bindHTTP, "bind-http", "", "override HTTP bind address (e.g. 0.0.0.0:8443)")
	cmd.Flags().StringVar(&ov.bindP2P, "bind-p2p", "", "override P2P UDP bind address")
	cmd.Flags().StringVar(&ov.publicHost, "public-host", "", "override externally-visible hostname")
	cmd.Flags().StringVar(&ov.tlsCert, "tls-cert", "", "override TLS cert path")
	cmd.Flags().StringVar(&ov.tlsKey, "tls-key", "", "override TLS key path")
	cmd.Flags().BoolVar(&ov.trustProxy, "trust-proxy", false, "honour X-Forwarded-* (override profile)")
	cmd.Flags().BoolVar(&ov.openBrowser, "open", true, "open the browser UI on start (loopback only)")
	cmd.Flags().BoolVarP(&ov.verbose, "verbose", "v", false, "verbose logs")
	cmd.Flags().StringArrayVar(&ov.bootstrap, "bootstrap", nil, "additional bootstrap peer (host:port; can be repeated)")

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

// runMessenger is the cobra-RunE callback. It owns ONLY: log-level
// setup, signal-aware ctx, mkdir, and dispatch to internal/app.Run.
// All orchestration logic lives in internal/app per CLAUDE.md.
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

	return app.Run(ctx, app.RunConfig{
		StorageDir:    storageDir,
		BindHTTP:      ov.bindHTTP,
		BindP2P:       ov.bindP2P,
		PublicHost:    ov.publicHost,
		TLSCert:       ov.tlsCert,
		TLSKey:        ov.tlsKey,
		TrustProxy:    ov.trustProxy,
		TrustProxySet: ov.trustProxySet,
		OpenBrowser:   ov.openBrowser,
		Bootstrap:     ov.bootstrap,
		Logger:        logger,
		LogLevel:      logLevel,
		OpenBrowserFn: openInBrowser,
		PrintBanner:   printRunBanner,
	})
}

func printRunBanner(p *config.Profile, url, idHash, fingerprint, udp string) {
	fmt.Println()
	fmt.Println("┌─ udisend messenger ──────────────────────────────────────────────")
	switch p.Mode {
	case config.ModeLoopback:
		fmt.Println("│  Open this URL in your browser (auth token included):")
	case config.ModePublicTailscale:
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
	case config.ModeLANIP, config.ModePublicAutocert, config.ModePublicProxy:
		if ips := localIPsForBanner(); len(ips) > 0 {
			fmt.Println("│")
			fmt.Println("│  Reachable on this host's IPs:")
			for _, ip := range ips {
				fmt.Println("│    -", ip)
			}
		}
	case config.ModePublicTailscale:
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
