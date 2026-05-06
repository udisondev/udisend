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

// TestChannel_CloseRacesRecv verifies that closing a channel while DATA frames
// are still in flight does not panic. Regression for the send-on-closed-channel
// race that existed when Close used to call close(c.inbox).
func TestChannel_CloseRacesRecv(t *testing.T) {
	t.Parallel()
	const iterations = 32
	for range iterations {
		runChannelCloseRaceOnce(t)
	}
}

func runChannelCloseRaceOnce(t *testing.T) {
	t.Helper()
	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makePeer(t, hub, resolver)
	b := makePeer(t, hub, resolver)
	resolver.put(makeRecord(t, a.id, a.t.LocalAddr().String()))
	resolver.put(makeRecord(t, b.id, b.t.LocalAddr().String()))

	incoming := make(chan *signaling.Channel, 1)
	b.svc.SetHandler(func(_ identity.Hash, ch *signaling.Channel) {
		incoming <- ch
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chA, err := a.svc.Connect(ctx, b.id.Public().DestinationHash())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	var chB *signaling.Channel
	select {
	case chB = <-incoming:
	case <-time.After(2 * time.Second):
		t.Fatal("incoming never fired")
	}

	var wg sync.WaitGroup
	wg.Add(3)
	// Sender pumping DATA frames toward chB.
	go func() {
		defer wg.Done()
		for range 16 {
			if err := chA.Send(ctx, []byte("payload")); err != nil {
				return
			}
		}
	}()
	// Reader on the receiving end.
	go func() {
		defer wg.Done()
		for {
			if _, err := chB.Recv(ctx); err != nil {
				return
			}
		}
	}()
	// Closer racing the above two.
	go func() {
		defer wg.Done()
		_ = chB.Close()
	}()
	wg.Wait()
	_ = chA.Close()
}
