package messenger_test

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/network"
)

// goRun launches a Run loop in a goroutine and reports unexpected
// non-cancel errors to the test via t.Errorf. Cancel is the normal exit
// path under t.Cleanup, so we filter it out.
func goRun(t *testing.T, name string, run func(context.Context) error, ctx context.Context) {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	t.Cleanup(func() {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("%s.Run: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s.Run did not exit after ctx cancel", name)
		}
	})
}

// openMessenger wires up a node + storage + messenger trio rooted at
// dir, registers cleanup, and starts the goroutines. Tests use this
// instead of duplicating the boilerplate in cmd/messenger/main.go.
func openMessenger(t *testing.T, ctx context.Context, dir string, bootstrap []string, outboxInterval time.Duration) *messenger.Messenger {
	t.Helper()

	store, err := storage.Open(ctx, filepath.Join(dir, "messenger.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})

	node, err := network.Open(ctx, network.Config{
		Identity:      mustIdentity(t),
		Listen:        "127.0.0.1:0",
		Bootstrap:     bootstrap,
		SeenPeerStore: store,
	})
	if err != nil {
		t.Fatalf("open network: %v", err)
	}
	t.Cleanup(func() {
		if err := node.Close(); err != nil {
			t.Errorf("close node: %v", err)
		}
	})

	mngr := messenger.Open(messenger.Config{
		Network:        node,
		Storage:        store,
		OutboxInterval: outboxInterval,
	})
	t.Cleanup(mngr.Close)

	goRun(t, "node", node.Run, ctx)
	goRun(t, "messenger", mngr.Run, ctx)

	return mngr
}

func TestOpen_StorageDirCreated(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mngr := openMessenger(t, ctx, t.TempDir(), nil, -1)

	if mngr.LocalAddress() == "" {
		t.Fatal("LocalAddress empty")
	}
	if mngr.Identity() == nil {
		t.Fatal("Identity nil")
	}
}

func TestRun_ExitsCleanlyOnContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "messenger.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})

	node, err := network.Open(ctx, network.Config{
		Identity: mustIdentity(t),
		Listen:   "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("open network: %v", err)
	}
	t.Cleanup(func() {
		if err := node.Close(); err != nil {
			t.Errorf("close node: %v", err)
		}
	})

	mngr := messenger.Open(messenger.Config{
		Network:        node,
		Storage:        store,
		OutboxInterval: -1,
	})
	t.Cleanup(mngr.Close)

	goRun(t, "node", node.Run, ctx)

	done := make(chan error, 1)
	go func() {
		done <- mngr.Run(ctx)
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned err: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestRemoveContact_CascadesAndClosesSession(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bob := openMessenger(t, ctx, t.TempDir(), nil, -1)
	alice := openMessenger(t, ctx, t.TempDir(), []string{bob.LocalAddress()}, -1)

	bobHash := bob.Identity().Public().DestinationHash()
	if err := alice.AddContact(ctx, bobHash, "bob"); err != nil {
		t.Fatalf("add contact: %v", err)
	}

	if _, err := alice.QueueOutbox(ctx, bobHash, []byte("queued-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.AppendHistory(ctx, storage.HistoryEntry{
		Peer:      bobHash,
		Direction: "out",
		Kind:      1,
		Body:      []byte("history-1"),
		When:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	sess, err := alice.Connect(ctx, bobHash)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	if err := alice.RemoveContact(ctx, bobHash, messenger.RemoveContactOptions{WipeHistory: true}); err != nil {
		t.Fatalf("remove contact: %v", err)
	}

	contacts, err := alice.Contacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range contacts {
		if c.Hash == bobHash {
			t.Fatalf("contact still present after RemoveContact")
		}
	}

	pending, err := alice.PendingOutbox(ctx, bobHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("outbox not cleared: %d items remain", len(pending))
	}

	hist, err := alice.History(ctx, bobHash, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 0 {
		t.Fatalf("history not wiped: %d entries remain", len(hist))
	}

	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("active session not closed by RemoveContact")
	}
}

func TestRemoveContact_KeepsHistory(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mngr := openMessenger(t, ctx, t.TempDir(), nil, -1)

	other, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer := other.Public().DestinationHash()
	if err := mngr.Storage().UpsertContact(ctx, storage.Contact{
		Hash:        peer,
		Public:      other.Public(),
		Alias:       "ghost",
		Fingerprint: other.Public().Fingerprint(),
		AddedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed contact: %v", err)
	}

	if _, err := mngr.AppendHistory(ctx, storage.HistoryEntry{
		Peer:      peer,
		Direction: "in",
		Kind:      1,
		Body:      []byte("memento"),
		When:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := mngr.RemoveContact(ctx, peer, messenger.RemoveContactOptions{WipeHistory: false}); err != nil {
		t.Fatalf("remove contact: %v", err)
	}

	hist, err := mngr.History(ctx, peer, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("history len = %d, want 1 (kept)", len(hist))
	}
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "messenger.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})

	node, err := network.Open(ctx, network.Config{
		Identity: mustIdentity(t),
		Listen:   "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("open network: %v", err)
	}
	t.Cleanup(func() {
		if err := node.Close(); err != nil {
			t.Errorf("close node: %v", err)
		}
	})

	mngr := messenger.Open(messenger.Config{
		Network:        node,
		Storage:        store,
		OutboxInterval: -1,
	})

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
