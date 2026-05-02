package messenger_test

import (
	"context"
	"crypto/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/pkg/identity"
)

// TestFlushOutboxOnce_OfflinePeerNotNotified — pump must NOT fire the
// peer-online handler when the recipient cannot be resolved.
func TestFlushOutboxOnce_OfflinePeerNotNotified(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	mngr, err := messenger.Open(ctx, messenger.Config{
		Identity:       mustIdentity(t),
		Listen:         "127.0.0.1:0",
		StorageDir:     t.TempDir(),
		OutboxInterval: -1, // pump disabled; we call FlushOutboxOnce directly
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mngr.Close)

	var fired atomic.Bool
	mngr.SetPeerOnlineHandler(func(_ identity.Hash) { fired.Store(true) })

	// Queue an outbox item for a totally unknown peer; no contact record →
	// ListContacts returns nothing → flush does no presence work, definitely
	// no notification.
	stranger := mustIdentity(t).Public().DestinationHash()
	if _, err := mngr.QueueOutbox(ctx, stranger, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	mngr.FlushOutboxOnce(ctx)
	if fired.Load() {
		t.Fatal("peer-online handler fired for unreachable peer")
	}
}

// TestFlushOutboxOnce_NotifiesOnlineRecipient — pump must fire the handler
// once for a contact whose presence resolves and that has pending items.
// Built as an integration test over UDP loopback because Open() pins
// transport.UDPTransport; AddContact's retry loop is the natural sync
// point — once it succeeds, the presence is freshly cached.
func TestFlushOutboxOnce_NotifiesOnlineRecipient(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	bob, err := messenger.Open(ctx, messenger.Config{
		Identity:       mustIdentity(t),
		Listen:         "127.0.0.1:0",
		StorageDir:     t.TempDir(),
		OutboxInterval: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bob.Close)
	go bob.Run(ctx)

	alice, err := messenger.Open(ctx, messenger.Config{
		Identity:       mustIdentity(t),
		Listen:         "127.0.0.1:0",
		StorageDir:     t.TempDir(),
		Bootstrap:      []string{bob.LocalAddress()},
		OutboxInterval: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(alice.Close)
	go alice.Run(ctx)

	bobHash := bob.Identity().Public().DestinationHash()

	// Force presence propagation by adding the contact (retries built-in).
	if err := alice.AddContact(ctx, bobHash, "bob"); err != nil {
		t.Fatalf("add contact: %v", err)
	}
	if _, err := alice.QueueOutbox(ctx, bobHash, []byte("hi")); err != nil {
		t.Fatal(err)
	}

	notified := make(chan identity.Hash, 1)
	alice.SetPeerOnlineHandler(func(h identity.Hash) {
		select {
		case notified <- h:
		default:
		}
	})

	alice.FlushOutboxOnce(ctx)
	select {
	case got := <-notified:
		if got != bobHash {
			t.Fatalf("notified for wrong peer: got %x want %x", got, bobHash)
		}
	case <-ctx.Done():
		t.Fatal("peer-online handler never fired before deadline")
	}
}

// reuse mustIdentity from messenger_test.go (same _test package).
var _ = identity.HashSize

// keep crypto/rand referenced if other tests in this file are removed.
var _ = rand.Reader
