package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/udisondev/udisend/internal/config"
)

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print a one-screen state snapshot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			storageDir, _ := cmd.Flags().GetString("storage")
			return runStatus(storageDir)
		},
	}
}

func runStatus(storageDir string) error {
	dbPath := filepath.Join(storageDir, "messenger.db")
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Printf("storage : %s\n", storageDir)
		fmt.Println("state   : not initialised — run `udisend public enable [addr]`")
		return nil
	}

	ctx, cancel, store, err := openStore(storageDir)
	if err != nil {
		return err
	}
	defer cancel()
	defer func() { _ = store.Close() }()

	prof, err := loadProfile(ctx, store)
	if err != nil {
		return err
	}
	creds, err := store.GetAuthCredentials(ctx)
	if err != nil {
		return err
	}
	recoveryLeft, err := store.CountUnconsumedRecoveryCodes(ctx)
	if err != nil {
		return err
	}

	idPath := filepath.Join(storageDir, "messenger.key")
	idHash := "(not generated)"
	idFp := ""
	if _, err := os.Stat(idPath); err == nil {
		id, err := config.LoadOrCreateIdentity(idPath)
		if err == nil {
			idHash = id.Public().DestinationHash().String()
			idFp = id.Public().Fingerprint()
		}
	}

	fmt.Println()
	fmt.Printf("storage     : %s\n", storageDir)
	fmt.Printf("identity    : %s\n", idHash)
	if idFp != "" {
		fmt.Printf("fingerprint : %s\n", idFp)
	}

	if prof == nil {
		fmt.Println("profile     : (none — run `udisend public enable [addr]`)")
	} else {
		fmt.Printf("profile     : %s\n", prof.Mode)
		fmt.Printf("  bind UI   : %s\n", prof.BindHTTP)
		fmt.Printf("  bind P2P  : %s\n", prof.BindP2P)
		if prof.PublicHost != "" {
			fmt.Printf("  URL       : https://%s/login\n", prof.PublicHost)
		}
		if prof.TLSCert != "" {
			if exp, err := readCertExpiry(prof.TLSCert); err == nil {
				left := time.Until(exp)
				fmt.Printf("  TLS cert  : %s (expires %s, %d days left)\n",
					prof.TLSCert, exp.UTC().Format("2006-01-02"), int(left.Hours()/24))
			} else {
				fmt.Printf("  TLS cert  : %s (read error: %v)\n", prof.TLSCert, err)
			}
		}
		if prof.TrustProxy {
			fmt.Println("  proxy     : trust-proxy enabled (X-Forwarded-For honoured)")
		}
	}

	fmt.Println()
	fmt.Println("auth")
	if creds == nil {
		fmt.Println("  passphrase: not set")
	} else {
		fmt.Println("  passphrase: ✓ set")
	}
	if creds != nil && creds.TOTPSecret != nil {
		fmt.Printf("  TOTP      : ✓ enrolled — %d/10 recovery codes left\n", recoveryLeft)
	} else {
		fmt.Println("  TOTP      : not enrolled")
	}
	fmt.Println()

	return nil
}
