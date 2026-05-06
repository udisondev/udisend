package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/udisondev/udisend/internal/storage"
)

// DisablePublic wipes the deployment profile so the next `udisend run`
// falls back to loopback. Auth credentials are NOT touched — use
// ResetState for the full "I lost my passphrase" escape hatch.
func DisablePublic(ctx context.Context, store *storage.Store) error {
	for _, k := range []string{
		SettingDeployMode,
		SettingDeployBindHTTP,
		SettingDeployBindP2P,
		SettingDeployPublicHost,
		SettingDeployTLSCert,
		SettingDeployTLSKey,
		SettingDeployTrustProxy,
	} {
		if err := store.DeleteSetting(ctx, k); err != nil {
			return err
		}
	}

	return nil
}

// ResetState clears auth credentials AND the deployment profile, plus
// best-effort deletion of TLS cert files, the autocert cache, and
// recovery-codes.txt. Identity (the Ed25519 keypair) is preserved.
//
// The caller must perform any user confirmation BEFORE invoking this —
// it is destructive by design and offers no rollback.
func ResetState(ctx context.Context, store *storage.Store, storageDir string) error {
	prof, _ := LoadProfile(ctx, store)
	if prof != nil {
		if prof.TLSCert != "" {
			_ = os.Remove(prof.TLSCert)
		}
		if prof.TLSKey != "" {
			_ = os.Remove(prof.TLSKey)
		}
	}
	_ = os.RemoveAll(filepath.Join(storageDir, "autocert"))

	if err := DisablePublic(ctx, store); err != nil {
		return err
	}
	if err := store.DeleteAuthCredentials(ctx); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(storageDir, "recovery-codes.txt"))

	return nil
}

// ErrAddressRequired is returned when ClassifyAddress is called with
// an empty addr in a mode that requires one (proxy, default).
var ErrAddressRequired = errors.New("config: address required")
