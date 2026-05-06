package signaling_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// TestService_CloseDoesNotDeadlockWithChannelClose drives Service.Close
// against concurrent Channel.Close calls — the AB/BA cycle previously
// possible (Service.Close held s.mu while invoking ch.shutdown(); a
// concurrent Channel.Close held closeOnce and waited on s.mu through
// service.unregister) is gone after Service.Close switched to the
// snapshot-then-shutdown pattern. Without the fix this test deadlocks
// reliably under -race.
func TestService_CloseDoesNotDeadlockWithChannelClose(t *testing.T) {
	t.Parallel()

	const iterations = 16
	for range iterations {
		runCloseRaceOnce(t)
	}
}

func runCloseRaceOnce(t *testing.T) {
	t.Helper()

	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makePeer(t, hub, resolver)
	b := makePeer(t, hub, resolver)
	resolver.put(makeRecord(t, a.id, a.t.LocalAddr().String()))
	resolver.put(makeRecord(t, b.id, b.t.LocalAddr().String()))

	incoming := make(chan *signaling.Channel, 1)
	b.svc.SetHandler(func(_ identity.Hash, ch *signaling.Channel) { incoming <- ch })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	chA, err := a.svc.Connect(ctx, b.id.Public().DestinationHash())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	var chB *signaling.Channel
	select {
	case chB = <-incoming:
	case <-ctx.Done():
		t.Fatal("incoming never fired")
	}

	// Race Channel.Close on B (acquires closeOnce, eventually wants s.mu)
	// against Service.Close on B (used to acquire s.mu and then wait for
	// closeOnce — the deadlock).
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = chB.Close() }()
	go func() { defer wg.Done(); b.svc.Close() }()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Service.Close + Channel.Close deadlocked")
	}

	_ = chA.Close()
}
