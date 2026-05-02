package config_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/udisondev/udisend/internal/config"
)

func TestLoadOrCreateIdentity_GeneratesAndPersists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "id.key")

	first, err := config.LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := config.LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if first.Public().DestinationHash() != second.Public().DestinationHash() {
		t.Fatalf("identity not stable across reloads")
	}
}

func TestLoadOrCreateIdentity_FileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode semantics differ on Windows")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "id.key")
	if _, err := config.LoadOrCreateIdentity(path); err != nil {
		t.Fatal(err)
	}
	got := statMode(t, path) & 0o777
	if got != 0o600 {
		t.Fatalf("identity file mode = %o, want 0600", got)
	}
}

func TestLoadOrCreateIdentity_RejectsEmptyPath(t *testing.T) {
	t.Parallel()
	if _, err := config.LoadOrCreateIdentity(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestLoadOrCreateIdentity_RejectsCorruptFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "id.key")
	if err := writeFile(path, []byte("not an identity")); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadOrCreateIdentity(path); err == nil {
		t.Fatal("expected parse error for corrupt seed")
	}
}
