package network_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/network"
	"github.com/udisondev/udisend/pkg/transport"
)

// goRun launches node.Run in a goroutine and reports any unexpected
// non-cancel error to the test. Tied to t.Cleanup so the assertion fires
// before the test exits.
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

// twoNodes spins up two loopback nodes; B is bootstrapped via A. Returns
// a-then-b. Both Run goroutines are tied to t.Cleanup.
func twoNodes(t *testing.T) (*network.Node, *network.Node) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	// t.Cleanup, not defer: cancel must outlive twoNodes (which
	// returns) and fire at test-end so the spawned Run goroutines see
	// it. defer here would cancel ctx before the test body even runs.
	t.Cleanup(cancel)

	// Run the two nodes over a shared MemoryHub. A real UDP socket
	// would force the test to poll on wall-clock time for DHT
	// convergence; over MemoryHub all goroutines stay schedulable on
	// the same Go runtime so observable progress is bounded by GC
	// and not by network round trips.
	hub := transport.NewMemoryHub()
	trA := hub.NewMemoryTransport()
	trB := hub.NewMemoryTransport()

	a, err := network.Open(ctx, network.Config{
		Identity:  mustIdentity(t),
		Transport: trA,
	})
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("close A: %v", err)
		}
	})

	goRun(t, "A", a.Run, ctx)

	b, err := network.Open(ctx, network.Config{
		Identity:  mustIdentity(t),
		Transport: trB,
		Bootstrap: []string{a.LocalAddress()},
	})
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("close B: %v", err)
		}
	})

	goRun(t, "B", b.Run, ctx)

	// Give the DHT a moment to exchange PING/NODES so each side can
	// resolve the other's presence record. Both directions must
	// converge — Phase 9 closed the cold-cache window in
	// verifySenderIdentity, so the responder side now refuses
	// channel install if it cannot resolve the initiator's hash.
	deadline := time.Now().Add(8 * time.Second)
	bothResolved := func() bool {
		lctx, lcancel := context.WithTimeout(ctx, 250*time.Millisecond)
		_, err := a.Lookup(lctx, b.Identity().Public().DestinationHash())
		lcancel()
		if err != nil {
			return false
		}
		lctx, lcancel = context.WithTimeout(ctx, 250*time.Millisecond)
		_, err = b.Lookup(lctx, a.Identity().Public().DestinationHash())
		lcancel()

		return err == nil
	}
	for time.Now().Before(deadline) {
		if bothResolved() {
			return a, b
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nodes never converged on each other's presence")

	return nil, nil
}

func TestNode_ConnectAndIncome(t *testing.T) {
	t.Parallel()

	a, b := twoNodes(t)
	bHash := b.Identity().Public().DestinationHash()
	aHash := a.Identity().Public().DestinationHash()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	sess, err := a.Connect(ctx, bHash)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if sess.Peer != bHash {
		t.Fatalf("session.Peer = %x, want %x", sess.Peer, bHash)
	}

	payload := []byte("hello from a")
	if err := sess.Send(ctx, payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// B should first receive the session-opened marker (Payload nil,
	// Final false), then the payload Income.
	gotPayload := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !gotPayload {
		select {
		case ev := <-b.Income():
			if ev == nil {
				t.Fatal("income returned nil")
			}
			if ev.Peer != aHash {
				t.Errorf("ev.Peer = %x, want %x", ev.Peer, aHash)
			}
			if ev.Final {
				ev.Release()
				t.Fatal("Final before payload")
			}
			if len(ev.Payload) == 0 {
				// session-opened marker — keep reading.
				ev.Release()

				continue
			}
			if !bytes.Equal(ev.Payload, payload) {
				t.Errorf("payload = %q, want %q", ev.Payload, payload)
			}
			ev.Release()
			gotPayload = true
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !gotPayload {
		t.Fatal("never received payload Income before deadline")
	}

	// Close the session — B should see Final.
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline = time.Now().Add(3 * time.Second)
	sawFinal := false
	for time.Now().Before(deadline) && !sawFinal {
		select {
		case ev := <-b.Income():
			if ev == nil {
				t.Fatal("income returned nil during shutdown")
			}
			if ev.Final {
				sawFinal = true
			}
			ev.Release()
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !sawFinal {
		t.Fatal("never saw Final income after Close")
	}
}

func TestNode_Lookup(t *testing.T) {
	t.Parallel()

	a, b := twoNodes(t)
	bHash := b.Identity().Public().DestinationHash()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	info, err := a.Lookup(ctx, bHash)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if info.Hash != bHash {
		t.Errorf("info.Hash = %x, want %x", info.Hash, bHash)
	}
	if info.Address == "" {
		t.Error("info.Address empty")
	}
	if len(info.Public.EdPub) == 0 {
		t.Error("info.Public.EdPub empty")
	}
}

func TestNode_SendUnknownSessionFails(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()

	a, err := network.Open(ctx, network.Config{
		Identity: mustIdentity(t),
		Listen:   "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	// No session installed. Send should refuse.
	var sid network.SessionID
	other := mustIdentity(t).Public().DestinationHash()
	err = a.Send(ctx, other, sid, []byte("x"))
	if !errors.Is(err, network.ErrUnknownSession) {
		t.Fatalf("Send: got %v, want ErrUnknownSession", err)
	}
}

func TestNode_SeenPeerStoreConsultedAndUpdated(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bootstrapNode, err := network.Open(ctx, network.Config{
		Identity: mustIdentity(t),
		Listen:   "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("open bootstrap: %v", err)
	}
	t.Cleanup(func() {
		if err := bootstrapNode.Close(); err != nil {
			t.Errorf("close bootstrap: %v", err)
		}
	})
	goRun(t, "bootstrap", bootstrapNode.Run, ctx)

	store := &fakeSeenPeerStore{
		entries: []string{bootstrapNode.LocalAddress()},
	}

	client, err := network.Open(ctx, network.Config{
		Identity:      mustIdentity(t),
		Listen:        "127.0.0.1:0",
		SeenPeerStore: store,
		// Bootstrap intentionally empty — must come from the store.
	})
	if err != nil {
		t.Fatalf("open client: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})
	goRun(t, "client", client.Run, ctx)

	if got := store.seenCalls.Load(); got != 1 {
		t.Errorf("SeenPeersDiverse called %d times, want 1", got)
	}

	// Wait for client to lookup bootstrap node — proves the cached
	// address actually drove the bootstrap loop.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if store.recordCalls.Load() > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("RecordSeenPeer never called — cached bootstrap address was not used")
}

// fakeSeenPeerStore is a minimal SeenPeerStore for tests.
type fakeSeenPeerStore struct {
	entries     []string
	seenCalls   atomic.Int64
	recordCalls atomic.Int64
	forgetCalls atomic.Int64
}

func (f *fakeSeenPeerStore) SeenPeersDiverse(_ context.Context, _ int) ([]string, error) {
	f.seenCalls.Add(1)

	return f.entries, nil
}

func (f *fakeSeenPeerStore) RecordSeenPeer(_ context.Context, _ string) error {
	f.recordCalls.Add(1)

	return nil
}

func (f *fakeSeenPeerStore) ForgetSeenPeer(_ context.Context, _ string) error {
	f.forgetCalls.Add(1)

	return nil
}
