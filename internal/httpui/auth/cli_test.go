package auth_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/httpui/auth"
	"github.com/udisondev/udisend/internal/storage"
)

func mkStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

func TestSetPassphrase_StoresHashAndVerifies(t *testing.T) {
	t.Parallel()
	store := mkStore(t)

	if err := auth.SetPassphrase(t.Context(), store, "hunter2-very-long-passphrase"); err != nil {
		t.Fatal(err)
	}

	creds, err := store.GetAuthCredentials(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil {
		t.Fatal("credentials not persisted")
	}
	ok, err := auth.VerifyPassphrase(creds.PassphraseHash, "hunter2-very-long-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("stored hash does not verify")
	}
}

func TestSetPassphrase_RejectsTooShort(t *testing.T) {
	t.Parallel()
	store := mkStore(t)

	for _, p := range []string{"", "short", "elevenchars"} {
		if err := auth.SetPassphrase(t.Context(), store, p); err == nil {
			t.Errorf("SetPassphrase(%q) accepted; expected length error", p)
		}
	}
}

func TestStartTOTPSetup_ProducesSecretAndURL(t *testing.T) {
	t.Parallel()

	secret, otpURL, err := auth.StartTOTPSetup("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != auth.TOTPSecretBytes {
		t.Errorf("secret length %d, want %d", len(secret), auth.TOTPSecretBytes)
	}
	if otpURL == "" || otpURL[:7] != "otpauth" {
		t.Errorf("URL malformed: %s", otpURL)
	}
}

func TestFinishTOTPSetup_RequiresPassphraseFirst(t *testing.T) {
	t.Parallel()
	store := mkStore(t)

	secret, _, _ := auth.StartTOTPSetup("alice@example.com")
	now := time.Unix(1700000000, 0).UTC()
	code := auth.CurrentTOTP(secret, now)
	_, err := auth.FinishTOTPSetup(t.Context(), store, secret, code, now)
	if err == nil {
		t.Errorf("FinishTOTPSetup before SetPassphrase should fail")
	}
}

func TestFinishTOTPSetup_StoresSecretAndRecoveryCodes(t *testing.T) {
	t.Parallel()
	store := mkStore(t)
	if err := auth.SetPassphrase(t.Context(), store, "hunter2-very-long-passphrase"); err != nil {
		t.Fatal(err)
	}

	secret, _, _ := auth.StartTOTPSetup("alice@example.com")
	now := time.Unix(1700000000, 0).UTC()
	code := auth.CurrentTOTP(secret, now)

	recovery, err := auth.FinishTOTPSetup(t.Context(), store, secret, code, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery) != auth.RecoveryCodeCount {
		t.Errorf("recovery len = %d, want %d", len(recovery), auth.RecoveryCodeCount)
	}

	creds, _ := store.GetAuthCredentials(t.Context())
	if creds.TOTPSecret == nil || len(creds.TOTPSecret) != len(secret) {
		t.Errorf("secret not stored")
	}
	rows, _ := store.UnconsumedRecoveryCodes(t.Context())
	if len(rows) != auth.RecoveryCodeCount {
		t.Errorf("recovery rows = %d, want %d", len(rows), auth.RecoveryCodeCount)
	}
}

func TestFinishTOTPSetup_RejectsWrongVerificationCode(t *testing.T) {
	t.Parallel()
	store := mkStore(t)
	if err := auth.SetPassphrase(t.Context(), store, "hunter2-very-long-passphrase"); err != nil {
		t.Fatal(err)
	}

	secret, _, _ := auth.StartTOTPSetup("alice@example.com")
	now := time.Unix(1700000000, 0).UTC()
	if _, err := auth.FinishTOTPSetup(t.Context(), store, secret, "000000", now); err == nil {
		t.Errorf("FinishTOTPSetup with wrong code should fail")
	}

	// Storage must be untouched on failed verification.
	creds, _ := store.GetAuthCredentials(t.Context())
	if creds.TOTPSecret != nil {
		t.Errorf("TOTP secret stored despite verification failure")
	}
}

func TestResetTOTP_ClearsSecretAndRecoveryCodes(t *testing.T) {
	t.Parallel()
	store := mkStore(t)
	if err := auth.SetPassphrase(t.Context(), store, "hunter2-very-long-passphrase"); err != nil {
		t.Fatal(err)
	}
	secret, _, _ := auth.StartTOTPSetup("alice@example.com")
	now := time.Unix(1700000000, 0).UTC()
	if _, err := auth.FinishTOTPSetup(t.Context(), store, secret, auth.CurrentTOTP(secret, now), now); err != nil {
		t.Fatal(err)
	}

	if err := auth.ResetTOTP(t.Context(), store); err != nil {
		t.Fatal(err)
	}

	creds, _ := store.GetAuthCredentials(t.Context())
	if creds.TOTPSecret != nil {
		t.Errorf("TOTP secret still present after reset")
	}
	rows, _ := store.UnconsumedRecoveryCodes(t.Context())
	if len(rows) != 0 {
		t.Errorf("recovery rows still present: %d", len(rows))
	}
}
