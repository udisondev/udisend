package messenger_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/pkg/identity"
)

func TestOpen_StorageDirCreated(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dir := filepath.Join(t.TempDir(), "fresh-subdir")
	id := mustIdentity(t)
	mngr, err := messenger.Open(ctx, messenger.Config{
		Identity:   id,
		Listen:     "127.0.0.1:0",
		StorageDir: dir,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer mngr.Close()
	if mngr.LocalAddress() == "" {
		t.Fatal("LocalAddress empty")
	}
	if mngr.Identity() == nil {
		t.Fatal("Identity nil")
	}
}

func TestRun_ExitsCleanlyOnContextCancel(t *testing.T) {
	t.Parallel()
	id := mustIdentity(t)
	ctx, cancel := context.WithCancel(context.Background())
	mngr, err := messenger.Open(ctx, messenger.Config{
		Identity:   id,
		Listen:     "127.0.0.1:0",
		StorageDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(mngr.Close)

	done := make(chan struct{})
	go func() {
		mngr.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := mustIdentity(t)
	mngr, err := messenger.Open(ctx, messenger.Config{
		Identity:   id,
		Listen:     "127.0.0.1:0",
		StorageDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mngr.Close()
	mngr.Close() // second close must not panic
}

func mustIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
