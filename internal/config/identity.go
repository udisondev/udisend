// Package config holds tiny helpers shared by both binaries — primarily
// loading or generating an Ed25519/X25519 identity backed by an on-disk
// seed file.
package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/udisondev/udisend/pkg/identity"
)

// LoadOrCreateIdentity reads the identity seed from `path`, or — if the
// file does not exist — generates a fresh identity, persists its seed,
// and returns it. The file is created with mode 0600 so the seed is
// not world-readable.
func LoadOrCreateIdentity(path string) (*identity.Identity, error) {
	if path == "" {
		return nil, errors.New("config: identity path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("config: mkdir for identity: %w", err)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var id identity.Identity
		if err := id.UnmarshalBinary(data); err != nil {
			return nil, fmt.Errorf("config: parse identity %q: %w", path, err)
		}
		return &id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config: read identity: %w", err)
	}
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		return nil, err
	}
	blob, err := id.MarshalBinary()
	if err != nil {
		return nil, err
	}

	// Atomic write: a crash during os.WriteFile would otherwise leave a
	// 0-byte / partial file at `path` that the next start would refuse
	// to parse. Write to .tmp and rename — Linux/macOS guarantee
	// rename atomicity within the same directory.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return nil, fmt.Errorf("config: write identity: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("config: rename identity: %w", err)
	}

	return id, nil
}
