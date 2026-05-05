package main

import (
	"bufio"
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/udisondev/udisend/internal/httpui/auth"
	"github.com/udisondev/udisend/internal/storage"
)

// runAuthSubcommand dispatches one of the auth-management flags. Exactly
// one of setPassword/setupTOTP/resetTOTP is expected to be true; the
// caller in run() guards that.
func runAuthSubcommand(storageDir string, setPassword, setupTOTP, resetTOTP bool) error {
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return fmt.Errorf("storage dir: %w", err)
	}
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(storageDir, "messenger.db"))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	switch {
	case setPassword:
		return setPassphraseInteractively(ctx, store)
	case setupTOTP:
		return setupTOTPInteractively(ctx, store)
	case resetTOTP:
		return auth.ResetTOTP(ctx, store)
	}

	return errors.New("no auth subcommand selected")
}

// setPassphraseInteractively reads the passphrase twice (no-echo) and
// confirms they match before persisting.
func setPassphraseInteractively(ctx context.Context, store *storage.Store) error {
	fmt.Println("Set webui passphrase. The terminal will not echo input.")
	first, err := readPassphrase("Passphrase: ")
	if err != nil {
		return err
	}
	second, err := readPassphrase("Confirm:    ")
	if err != nil {
		return err
	}
	if first != second {
		return errors.New("passphrases do not match")
	}

	if err := auth.SetPassphrase(ctx, store, first); err != nil {
		return err
	}
	fmt.Println("Passphrase saved.")

	return nil
}

// setupTOTPInteractively walks the operator through TOTP enrollment:
// generate secret → print URL/secret for the authenticator → prompt for
// the current code to confirm → save + print recovery codes once.
func setupTOTPInteractively(ctx context.Context, store *storage.Store) error {
	creds, err := store.GetAuthCredentials(ctx)
	if err != nil {
		return err
	}
	if creds == nil {
		return errors.New("set the passphrase first: messenger -set-password")
	}

	secret, otpURL, err := auth.StartTOTPSetup("messenger")
	if err != nil {
		return err
	}

	secretBase32 := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	fmt.Println()
	fmt.Println("Add the following entry to your authenticator app:")
	fmt.Println()
	fmt.Println("  URL    :", otpURL)
	fmt.Println("  Secret :", secretBase32)
	fmt.Println()
	fmt.Println("If your app accepts manual entry, the algorithm is SHA1, period 30s, 6 digits.")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter the 6-digit code from your authenticator: ")
	code, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	code = strings.TrimSpace(code)

	recovery, err := auth.FinishTOTPSetup(ctx, store, secret, code, time.Now())
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("TOTP enrolled. Save the recovery codes below — they will not be shown again:")
	fmt.Println()
	for _, c := range recovery {
		fmt.Println("  ", c)
	}
	fmt.Println()
	fmt.Println("Each code works exactly once and replaces the TOTP step on a single login.")

	return nil
}

// readPassphrase reads from the controlling TTY without echo. Falls back
// to line-buffered stdin when there is no TTY (CI, piped input).
func readPassphrase(prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}

		return string(b), nil
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}

	return strings.TrimRight(line, "\r\n"), nil
}
