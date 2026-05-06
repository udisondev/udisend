package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/udisondev/udisend/internal/storage"
)

// MinPassphraseLen is the lower bound the CLI helpers enforce. Modelled
// on NIST SP 800-63B which favours length over composition rules — 12
// chars is the modern lower bound for a single-factor passphrase.
const MinPassphraseLen = 12

// SetPassphrase hashes p with Argon2id and writes it to the store. Used
// by the CLI subcommand `messenger -set-password`.
func SetPassphrase(ctx context.Context, store *storage.Store, p string) error {
	if len(p) < MinPassphraseLen {
		return fmt.Errorf("passphrase must be at least %d characters", MinPassphraseLen)
	}
	hash, err := HashPassphrase(p)
	if err != nil {
		return err
	}

	return store.SetPassphrase(ctx, hash)
}

// StartTOTPSetup generates a fresh TOTP secret and the otpauth:// URL the
// user should scan with their authenticator app. The secret is NOT yet
// persisted — call FinishTOTPSetup after the user proves possession by
// typing back a valid code.
func StartTOTPSetup(account string) ([]byte, string, error) {
	secret, _, err := GenerateTOTPSecret()
	if err != nil {
		return nil, "", err
	}

	return secret, OTPAuthURL(secret, "udisend", account), nil
}

// FinishTOTPSetup verifies that the user really has the secret (their
// authenticator typed back a current code), then atomically stores the
// secret and rotates a fresh batch of recovery codes. Returns the
// plaintext recovery codes — the caller MUST display them to the user
// once and never store them.
//
// On verification failure, nothing is persisted.
func FinishTOTPSetup(ctx context.Context, store *storage.Store, secret []byte, verifyCode string, now time.Time) ([]string, error) {
	creds, err := store.GetAuthCredentials(ctx)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return nil, errors.New("auth: passphrase not set; run -set-password first")
	}

	if !VerifyTOTP(secret, verifyCode, now) {
		return nil, errors.New("auth: TOTP verification failed; check your authenticator's time and try again")
	}

	if err := store.SetTOTPSecret(ctx, secret); err != nil {
		return nil, err
	}

	codes, err := GenerateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		h, err := HashRecoveryCode(c)
		if err != nil {
			return nil, err
		}
		hashes[i] = h
	}
	if err := store.ResetRecoveryCodes(ctx, hashes); err != nil {
		return nil, err
	}

	return codes, nil
}

// ResetTOTP clears the TOTP secret and any unconsumed recovery codes.
// Used to disenroll the second factor — passphrase remains in place.
func ResetTOTP(ctx context.Context, store *storage.Store) error {
	if err := store.ClearTOTPSecret(ctx); err != nil {
		return err
	}

	return store.ResetRecoveryCodes(ctx, nil)
}
