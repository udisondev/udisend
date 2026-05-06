package main

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/udisondev/udisend/internal/config"
	"github.com/udisondev/udisend/internal/httpui/auth"
	"github.com/udisondev/udisend/internal/storage"
	tspkg "github.com/udisondev/udisend/pkg/tailscale"
)

// newPublicCommand builds the `udisend public` parent and its three
// children: enable / disable / reset.
func newPublicCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "public",
		Short: "Switch between loopback and public deployment modes",
	}
	cmd.AddCommand(
		newPublicEnableCommand(),
		newPublicDisableCommand(),
		newPublicResetCommand(),
	)
	return cmd
}

func newPublicEnableCommand() *cobra.Command {
	var (
		viaProxy bool
		httpPort string
		p2pPort  string
	)
	cmd := &cobra.Command{
		Use:   "enable [addr]",
		Short: "Switch to public mode",
		Long: `Switch to a public deployment mode and prompt for auth.

Without an address, auto-detects Tailscale on this host: hostname comes
from "tailscale status", cert from Tailscale's Let's Encrypt pipeline.
This gives you a real (browser-trusted) cert with no extra config.

Address shapes:
  - omitted              → Tailscale (must have tailscaled running + logged in)
  - IP literal           → LAN mode with auto-generated self-signed cert
  - DNS name             → autocert (Let's Encrypt via embedded ACME, port 80)
  - DNS name with --proxy → public-proxy mode (Caddy/nginx terminates TLS)

Examples:
  udisend public enable
  udisend public enable 192.168.0.105
  udisend public enable udisend.example.com
  udisend public enable udisend.example.com --proxy`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			storageDir, _ := cmd.Flags().GetString("storage")
			addr := ""
			if len(args) == 1 {
				addr = args[0]
			}
			// Empty addr + no --proxy = Tailscale auto-mode.
			viaTailscale := addr == "" && !viaProxy
			return runEnable(storageDir, addr, viaProxy, viaTailscale, httpPort, p2pPort)
		},
	}
	cmd.Flags().BoolVar(&viaProxy, "proxy", false, "deploy behind a reverse proxy that terminates TLS (Caddy/nginx)")
	cmd.Flags().StringVar(&httpPort, "port", defaultHTTPPort, "HTTP port (autocert mode forces 443)")
	cmd.Flags().StringVar(&p2pPort, "p2p-port", defaultP2PPort, "P2P UDP port")

	return cmd
}

func newPublicDisableCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Revert to loopback mode (auth credentials preserved)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			storageDir, _ := cmd.Flags().GetString("storage")
			ctx, cancel, store, err := openStore(storageDir)
			if err != nil {
				return err
			}
			defer cancel()
			defer func() { _ = store.Close() }()

			return disablePublic(ctx, store)
		},
	}
}

func newPublicResetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Wipe auth credentials AND profile (identity preserved)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			storageDir, _ := cmd.Flags().GetString("storage")
			ctx, cancel, store, err := openStore(storageDir)
			if err != nil {
				return err
			}
			defer cancel()
			defer func() { _ = store.Close() }()

			return resetEverything(ctx, store, storageDir)
		},
	}
}

// disablePublic is a thin UI wrapper around config.DisablePublic that
// adds the operator-facing confirmation line. State mutation lives in
// internal/config per CLAUDE.md cmd/-minimality rule.
func disablePublic(ctx context.Context, store *storage.Store) error {
	if err := config.DisablePublic(ctx, store); err != nil {
		return err
	}
	fmt.Println("✓ public mode disabled — `udisend run` will start in loopback")

	return nil
}

// resetEverything is the "I lost my passphrase / TOTP" escape hatch.
// The interactive confirmation prompt stays here (UI concern); the
// destructive state cleanup is delegated to config.ResetState.
func resetEverything(ctx context.Context, store *storage.Store, storageDir string) error {
	fmt.Println("This will:")
	fmt.Println("  - clear the webui passphrase")
	fmt.Println("  - clear TOTP enrollment and recovery codes")
	fmt.Println("  - clear the deployment profile (mode reverts to loopback)")
	fmt.Println("  - delete persisted TLS cert files (if any)")
	fmt.Println("Identity (the P2P keypair) is preserved.")
	fmt.Println()

	pr := newPrompter(os.Stdin, os.Stdout)
	ok, err := pr.readYesNo("Proceed?", false)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("cancelled")
	}

	if err := config.ResetState(ctx, store, storageDir); err != nil {
		return err
	}

	fmt.Println("✓ everything cleared. Run `udisend public enable [addr]` to set up again.")

	return nil
}

// runEnable classifies the address (or auto-detects via Tailscale),
// builds the matching profile, prompts for passphrase + TOTP,
// generates a cert if needed, and persists everything.
func runEnable(storageDir, addr string, viaProxy, viaTailscale bool, httpPort, p2pPort string) error {
	ctx, cancel, store, err := openStore(storageDir)
	if err != nil {
		return err
	}
	defer cancel()
	defer func() { _ = store.Close() }()

	prof := &profile{
		BindP2P: "0.0.0.0:" + p2pPort,
	}

	switch {
	case viaTailscale:
		ts := tspkg.New()
		if err := ts.Detect(ctx); err != nil {
			return fmt.Errorf("Tailscale not usable: %w\n  Hint: install Tailscale (https://tailscale.com/download) and `tailscale up`, then re-run.\n        Or pass an address explicitly: `udisend public enable <ip|host>`", err)
		}
		host, err := ts.Hostname(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("✓ Tailscale detected — using %s\n\n", host)
		prof.Mode = modePublicTailscale
		prof.BindHTTP = "0.0.0.0:" + httpPort
		prof.PublicHost = host
		// Pre-flight cert request to make sure the tailnet has HTTPS
		// enabled before we save the profile and prompt for passphrase.
		if _, err := ts.CertWithDeadline(ctx, host, 90*time.Second); err != nil {
			return fmt.Errorf("Tailscale cert: %w\n  Hint: enable HTTPS in your tailnet admin (https://login.tailscale.com/admin/dns#https) and re-run", err)
		}

	case viaProxy:
		if addr == "" {
			return errors.New("--proxy requires a hostname argument (the public DNS name your proxy serves)")
		}
		host, port, err := splitHostPort(addr, httpPort)
		if err != nil {
			return err
		}
		prof.Mode = modePublicProxy
		prof.BindHTTP = loopbackHost + ":" + port
		prof.PublicHost = host
		prof.TrustProxy = true

	default:
		host, port, err := splitHostPort(addr, httpPort)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip != nil {
			prof.Mode = modeLANIP
			prof.BindHTTP = "0.0.0.0:" + port
			prof.PublicHost = net.JoinHostPort(host, port)
			prof.TLSCert = filepath.Join(storageDir, "tls.crt")
			prof.TLSKey = filepath.Join(storageDir, "tls.key")
		} else {
			// DNS → autocert. ACME HTTP-01 needs port 80; TLS lives on 443.
			prof.Mode = modePublicAutocert
			prof.BindHTTP = "0.0.0.0:443"
			prof.PublicHost = host
		}
	}

	pr := newPrompter(os.Stdin, os.Stdout)

	fmt.Printf("Set a passphrase (≥%d chars, will not echo)\n", auth.MinPassphraseLen)
	pass, err := pr.readPassphraseTwice(auth.MinPassphraseLen)
	if err != nil {
		return err
	}
	if err := auth.SetPassphrase(ctx, store, pass); err != nil {
		return err
	}
	fmt.Println("✓ passphrase saved")
	fmt.Println()

	enrollTOTP, err := pr.readYesNo("Enable TOTP second factor?", true)
	if err != nil {
		return err
	}
	if enrollTOTP {
		if err := enrollTOTPInteractive(ctx, store, pr, storageDir); err != nil {
			return err
		}
	}

	if prof.Mode == modeLANIP {
		fmt.Printf("Generating self-signed TLS cert for %s (365 days)...\n", prof.PublicHost)
		if err := generateSelfSignedCert(prof.PublicHost, prof.TLSCert, prof.TLSKey); err != nil {
			return err
		}
		fmt.Printf("✓ saved to %s + %s\n", prof.TLSCert, prof.TLSKey)
		fmt.Println()
	}

	if err := prof.Validate(true); err != nil {
		return err
	}
	if err := saveProfile(ctx, store, prof); err != nil {
		return err
	}

	printEnableSummary(prof)

	return nil
}

// splitHostPort splits an addr (with optional port) into host + port,
// applying defaultPort when none is given. Bare IPv4, hostnames, IP:port,
// [ipv6]:port, and bare IPv6 all round-trip cleanly.
func splitHostPort(addr, defaultPort string) (string, string, error) {
	if addr == "" {
		return "", "", errors.New("address required")
	}
	addr = strings.TrimSpace(addr)
	if host, port, err := net.SplitHostPort(addr); err == nil {
		return host, port, nil
	}
	if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
		return addr, defaultPort, nil
	}
	return addr, defaultPort, nil
}

func printEnableSummary(p *profile) {
	fmt.Println("┌─ public mode enabled ──────────────────────────────────────────")
	fmt.Printf("│  mode    : %s\n", p.Mode)
	fmt.Printf("│  bind UI : %s\n", p.BindHTTP)
	fmt.Printf("│  bind P2P: %s\n", p.BindP2P)
	switch p.Mode {
	case modeLANIP:
		fmt.Printf("│  URL     : https://%s/login\n", p.PublicHost)
		fmt.Println("│  cert    : self-signed — browsers will warn once")
	case modePublicAutocert:
		fmt.Printf("│  URL     : https://%s/login\n", p.PublicHost)
		fmt.Println("│  cert    : Let's Encrypt via embedded autocert (auto-renewing)")
		fmt.Println("│  port 80 : must be reachable for ACME HTTP-01 challenge")
	case modePublicProxy:
		fmt.Printf("│  URL     : https://%s/login (proxy → %s)\n", p.PublicHost, p.BindHTTP)
		fmt.Println("│  cert    : terminated by your reverse proxy")
	case modePublicTailscale:
		fmt.Printf("│  URL     : https://%s:%s/login\n", p.PublicHost, portFromBindHTTP(p.BindHTTP))
		fmt.Println("│  cert    : Let's Encrypt via Tailscale (auto-renewing)")
		fmt.Println("│  access  : any device in your tailnet (Tailscale installed + same account)")
	}
	fmt.Println("│")
	fmt.Println("│  Run:")
	fmt.Println("│    udisend run")
	fmt.Println("└────────────────────────────────────────────────────────────────")
}

func portFromBindHTTP(bind string) string {
	_, port, err := net.SplitHostPort(bind)
	if err != nil {
		return bind
	}
	return port
}

// enrollTOTPInteractive runs the TOTP enrollment side-flow during enable.
func enrollTOTPInteractive(ctx context.Context, store *storage.Store, pr *prompter, storageDir string) error {
	secret, otpURL, err := auth.StartTOTPSetup("messenger")
	if err != nil {
		return err
	}
	secretBase32 := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)

	fmt.Println()
	fmt.Println("Scan this QR with your authenticator app:")
	fmt.Println()
	if err := renderQR(os.Stdout, otpURL); err != nil {
		fmt.Println("(QR render failed — paste the secret below)")
	}
	fmt.Println()
	fmt.Println("  Or enter manually:")
	fmt.Printf("    Secret    : %s\n", formatSecret(secretBase32))
	fmt.Println("    Algorithm : SHA1, 30s, 6 digits")
	fmt.Println()

	code, err := pr.readLine("Code from your authenticator: ")
	if err != nil {
		return err
	}
	code = strings.TrimSpace(code)

	recovery, err := auth.FinishTOTPSetup(ctx, store, secret, code, time.Now())
	if err != nil {
		return err
	}
	fmt.Println("✓ TOTP enrolled")
	fmt.Println()

	if err := writeRecoveryCodes(storageDir, recovery, os.Stdout); err != nil {
		return err
	}

	_, _ = pr.readLine("Press Enter when you have stored the recovery codes... ")
	fmt.Println()

	return nil
}

func formatSecret(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func writeRecoveryCodes(storageDir string, codes []string, w io.Writer) error {
	path := filepath.Join(storageDir, "recovery-codes.txt")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	header := "udisend recovery codes — each works exactly once.\n" +
		"Move this file to durable offline storage and delete the copy here.\n\n"
	if _, err := io.WriteString(f, header); err != nil {
		return err
	}

	fmt.Fprintln(w, "Recovery codes (each works once if you lose your authenticator):")
	fmt.Fprintln(w)
	for _, c := range codes {
		fmt.Fprintf(w, "    %s\n", c)
		if _, err := fmt.Fprintln(f, c); err != nil {
			return err
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  Saved to: %s (mode 0600)\n", path)
	fmt.Fprintln(w, "  After you've stored them safely, `rm` that file.")
	fmt.Fprintln(w)

	return nil
}

// openStore opens the SQLite store and returns a context that cancels
// on SIGINT/SIGTERM. Caller must invoke the returned cancel to release
// the signal handler goroutine.
func openStore(storageDir string) (context.Context, context.CancelFunc, *storage.Store, error) {
	if err := os.MkdirAll(storageDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("storage dir: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	store, err := storage.Open(ctx, filepath.Join(storageDir, "messenger.db"))
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	return ctx, cancel, store, nil
}
