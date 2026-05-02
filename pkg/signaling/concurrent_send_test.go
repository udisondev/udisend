package signaling_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// TestChannel_ConcurrentSendIsRaceFree drives N parallel Sends through
// the same Channel and asserts every payload decrypts cleanly on the
// other side. flynn/noise's AEAD nonce counter advances inside Encrypt
// and is NOT goroutine-safe, so without sendMu this test (under -race)
// either flags a data race on the cipher state or — worse — produces
// duplicate-nonce ciphertext that the peer rejects with a "decrypt
// failed" Debug log.
func TestChannel_ConcurrentSendIsRaceFree(t *testing.T) {
	t.Parallel()

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
	defer chA.Close()

	var chB *signaling.Channel
	select {
	case chB = <-incoming:
	case <-ctx.Done():
		t.Fatal("incoming never fired")
	}
	defer chB.Close()

	// Drain receiver concurrently — accept whatever arrives.
	const n = 32
	got := make(chan string, n)
	go func() {
		for range n {
			msg, err := chB.Recv(ctx)
			if err != nil {
				return
			}
			got <- string(msg)
		}
	}()

	// Fan out N concurrent sends; tag each payload uniquely so we can
	// confirm the receiver got *every* one (no nonce collisions, no drops).
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()

			payload := "payload-" + strconv.Itoa(seq)
			if err := chA.Send(ctx, []byte(payload)); err != nil {
				t.Errorf("send %d: %v", seq, err)
			}
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	for range n {
		select {
		case s := <-got:
			seen[s] = true
		case <-ctx.Done():
			t.Fatalf("receiver only got %d/%d payloads before deadline", len(seen), n)
		}
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct payloads, want %d", len(seen), n)
	}
}
