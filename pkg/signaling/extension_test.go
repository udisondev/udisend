package signaling_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// TestChannel_SendExtension_RoundTrip verifies the embedder API:
// an embedder picks any kind in the embedder range (>= 0x06), sends
// ciphertext through Channel.SendExtension on one side, and receives
// the decrypted payload through SetExtensionHandler on the other.
// signaling does not know about specific embedder opcodes; the test
// uses a generic kind 0x06 to make the contract explicit.
func TestChannel_SendExtension_RoundTrip(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makePeer(t, hub, resolver)
	b := makePeer(t, hub, resolver)
	resolver.put(makeRecord(t, a.id, a.t.LocalAddr().String()))
	resolver.put(makeRecord(t, b.id, b.t.LocalAddr().String()))

	type extMsg struct {
		peer identity.Hash
		kind byte
		buf  []byte
	}
	got := make(chan extMsg, 1)

	type incoming struct {
		peer identity.Hash
		ch   *signaling.Channel
	}
	in := make(chan incoming, 1)
	b.svc.SetHandler(func(peer identity.Hash, ch *signaling.Channel) {
		in <- incoming{peer: peer, ch: ch}
	})
	b.svc.SetExtensionHandler(func(ch *signaling.Channel, kind byte, payload []byte) {
		got <- extMsg{peer: ch.Peer(), kind: kind, buf: payload}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()

	chA, err := a.svc.Connect(ctx, b.id.Public().DestinationHash())
	if err != nil {
		t.Fatalf("a.Connect: %v", err)
	}

	select {
	case <-in:
	case <-time.After(2 * time.Second):
		t.Fatal("B never observed incoming channel")
	}

	const embedderKind byte = 0x06
	body := []byte("payload-from-embedder")
	if err := chA.SendExtension(ctx, embedderKind, body); err != nil {
		t.Fatalf("SendExtension: %v", err)
	}

	select {
	case msg := <-got:
		if msg.kind != embedderKind {
			t.Errorf("kind = %d, want %d", msg.kind, embedderKind)
		}
		if string(msg.buf) != string(body) {
			t.Errorf("payload = %q, want %q", msg.buf, body)
		}
		if msg.peer != a.id.Public().DestinationHash() {
			t.Errorf("peer = %x, want %x", msg.peer, a.id.Public().DestinationHash())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("B never received extension frame")
	}
}

// TestChannel_SendExtension_RejectsInternalKinds ensures the API
// surface refuses kinds in the signaling-internal range (<= 0x05).
// Without this check, application traffic could be smuggled through
// SendExtension and bypass handleData's noise-decrypt path.
func TestChannel_SendExtension_RejectsInternalKinds(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makePeer(t, hub, resolver)
	b := makePeer(t, hub, resolver)
	resolver.put(makeRecord(t, a.id, a.t.LocalAddr().String()))
	resolver.put(makeRecord(t, b.id, b.t.LocalAddr().String()))

	in := make(chan struct{}, 1)
	b.svc.SetHandler(func(_ identity.Hash, _ *signaling.Channel) { in <- struct{}{} })

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()

	chA, err := a.svc.Connect(ctx, b.id.Public().DestinationHash())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	<-in

	tests := []struct {
		name string
		kind byte
	}{
		{name: "hello init (0x01)", kind: 0x01},
		{name: "hello resp (0x02)", kind: 0x02},
		{name: "hello final (0x03)", kind: 0x03},
		{name: "data (0x04)", kind: 0x04},
		{name: "bye (0x05)", kind: 0x05},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := chA.SendExtension(ctx, tt.kind, []byte("x"))
			if err == nil {
				t.Errorf("SendExtension(0x%02x) = nil, want error", tt.kind)
			}
		})
	}
}

// TestSetExtensionHandler_NilDisables verifies that passing nil clears
// the handler — operationally needed for clean shutdown of an embedder
// before service.Close.
func TestSetExtensionHandler_NilDisables(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makePeer(t, hub, resolver)
	b := makePeer(t, hub, resolver)
	resolver.put(makeRecord(t, a.id, a.t.LocalAddr().String()))
	resolver.put(makeRecord(t, b.id, b.t.LocalAddr().String()))

	var (
		mu       sync.Mutex
		received int
	)
	b.svc.SetExtensionHandler(func(_ *signaling.Channel, _ byte, _ []byte) {
		mu.Lock()
		received++
		mu.Unlock()
	})

	in := make(chan *signaling.Channel, 1)
	b.svc.SetHandler(func(_ identity.Hash, ch *signaling.Channel) { in <- ch })

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()

	chA, err := a.svc.Connect(ctx, b.id.Public().DestinationHash())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	<-in

	b.svc.SetExtensionHandler(nil)

	if err := chA.SendExtension(ctx, 0x06, []byte("ignored")); err != nil {
		t.Fatalf("SendExtension: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if received != 0 {
		t.Errorf("received %d callbacks after nil-disable, want 0", received)
	}
}

// TestConnect_BogusPeer_NoPanic guards against a panic regression
// (originally introduced and fixed in Phase 10.3) where Connect to a
// peer with no presence record could deref a nil pointer. The test
// asserts a typed error, not a context-cancel.
func TestConnect_BogusPeer_NoPanic(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makePeer(t, hub, resolver)
	resolver.put(makeRecord(t, a.id, a.t.LocalAddr().String()))

	bogus := identity.Hash{0xff, 0xff}
	_, err := a.svc.Connect(t.Context(), bogus)
	if err == nil {
		t.Fatal("Connect to unknown peer succeeded; want error")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected ctx cancel: %v", err)
	}
}
