package signaling_test

import (
	"context"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// TestReplay_DataFrameRejected runs a real wire-level replay attack:
// installs a MITM transport in front of B, captures the encrypted DATA
// envelope as it transits the hub, restores B's real transport,
// re-injects the captured envelope from a third (attacker) transport,
// and asserts B does NOT surface a second copy through chB.Recv.
// Replay-rejection is provided by the Noise XK AEAD nonce counter on
// the responder side: any ciphertext at a counter ≤ the highest one
// already accepted fails to decrypt.
func TestReplay_DataFrameRejected(t *testing.T) {
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
		t.Fatal(err)
	}
	defer chA.Close()

	var chB *signaling.Channel
	select {
	case chB = <-incoming:
	case <-ctx.Done():
		t.Fatal("incoming never fired")
	}
	defer chB.Close()

	// Sync point: pump one message end-to-end so the handshake is
	// definitively past and both noise counters agree.
	if err := chA.Send(ctx, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := chB.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("warm-up: got %q, want hello", got)
	}

	// Install a MITM in front of B: a fresh transport bound to the
	// same hub id; Swap atomically replaces B's registration so the
	// next hop from A lands here. We hold a reference to B's original
	// transport so we can both restore it and re-inject the captured
	// envelope to it later.
	bAddr := b.t.LocalAddr().(transport.MemoryAddr)
	mitm := hub.NewMemoryTransport()
	t.Cleanup(func() { _ = mitm.Close() })
	prev := hub.Swap(bAddr.ID(), mitm)
	if prev == nil {
		t.Fatalf("no transport registered at id %q", bAddr.ID())
	}

	if err := chA.Send(ctx, []byte("captured")); err != nil {
		t.Fatal(err)
	}

	var captured []byte
	select {
	case pkt := <-mitm.Inbox():
		captured = pkt.Payload
	case <-ctx.Done():
		t.Fatal("MITM never observed the envelope")
	}

	// Restore B's real transport so subsequent traffic flows normally.
	hub.Swap(bAddr.ID(), prev)

	// Forward the just-captured envelope to B for the first time so
	// the genuine "captured" payload gets decrypted and surfaced.
	attacker := hub.NewMemoryTransport()
	t.Cleanup(func() { _ = attacker.Close() })
	if err := attacker.Send(ctx, b.t.LocalAddr(), captured); err != nil {
		t.Fatal(err)
	}
	got, err = chB.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "captured" {
		t.Fatalf("genuine forward: got %q, want captured", got)
	}

	// REPLAY: send the same captured bytes again. Noise's nonce
	// counter on B has already advanced past this ciphertext, so the
	// decrypt MUST fail and chB.Recv MUST NOT surface a second copy.
	if err := attacker.Send(ctx, b.t.LocalAddr(), captured); err != nil {
		t.Fatal(err)
	}

	// Synchronise on a follow-up genuine message: if Recv yields
	// "after-replay" next, the replay was dropped (correct). If it
	// yields "captured" again, the replay leaked through.
	if err := chA.Send(ctx, []byte("after-replay")); err != nil {
		t.Fatal(err)
	}
	final, err := chB.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(final) != "after-replay" {
		t.Fatalf("replay was not rejected: got %q, want after-replay", final)
	}
}

// TestReplay_DuplicateHelloInitDropped is unchanged in intent — a
// HELLO_INIT replayed against an already-open session must not spawn
// a second incoming-handler call.
func TestReplay_DuplicateHelloInitDropped(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makePeer(t, hub, resolver)
	b := makePeer(t, hub, resolver)
	resolver.put(makeRecord(t, a.id, a.t.LocalAddr().String()))
	resolver.put(makeRecord(t, b.id, b.t.LocalAddr().String()))

	incoming := make(chan *signaling.Channel, 4)
	b.svc.SetHandler(func(_ identity.Hash, ch *signaling.Channel) { incoming <- ch })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	chA, err := a.svc.Connect(ctx, b.id.Public().DestinationHash())
	if err != nil {
		t.Fatal(err)
	}
	defer chA.Close()
	select {
	case <-incoming:
	case <-ctx.Done():
		t.Fatal("first session never accepted")
	}

	chA2, err := a.svc.Connect(ctx, b.id.Public().DestinationHash())
	if err != nil {
		t.Fatal(err)
	}
	defer chA2.Close()
	select {
	case <-incoming:
	case <-ctx.Done():
		t.Fatal("second session never accepted")
	}
	select {
	case extra := <-incoming:
		t.Fatalf("unexpected third incoming session (replay leaked): %v", extra)
	default:
	}
}
