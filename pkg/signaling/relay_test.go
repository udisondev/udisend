package signaling_test

import (
	"context"
	"crypto/rand"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// fakeRouter resolves recipient → next-hop via an explicit table and
// publishes every NextHop call on `calls` so tests can synchronise on
// dispatcher progress without sleeping.
type fakeRouter struct {
	mu    sync.Mutex
	table map[identity.Hash]net.Addr
	calls chan identity.Hash
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{
		table: make(map[identity.Hash]net.Addr),
		calls: make(chan identity.Hash, 16),
	}
}

func (f *fakeRouter) put(target identity.Hash, addr net.Addr) {
	f.mu.Lock()
	f.table[target] = addr
	f.mu.Unlock()
}

func (f *fakeRouter) NextHop(_ context.Context, target identity.Hash) (net.Addr, bool) {
	f.mu.Lock()
	addr, ok := f.table[target]
	f.mu.Unlock()
	select {
	case f.calls <- target:
	default:
	}
	return addr, ok
}

// TestRelay_ForwardsToNextHop verifies design.md §4: a node receiving an
// envelope whose recipient is not itself forwards it to the closest
// next-hop. We build A → R → B where A's resolver gives B's address
// pointing at the relay; the relay's Router translates the recipient
// hash to B's real address. A successful Connect proves the chain.
func TestRelay_ForwardsToNextHop(t *testing.T) {
	t.Parallel()
	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}

	a := makePeer(t, hub, resolver)
	relay := makePeer(t, hub, resolver)
	b := makePeer(t, hub, resolver)

	resolver.put(makeRecord(t, a.id, a.t.LocalAddr().String()))
	resolver.put(makeRecord(t, b.id, relay.t.LocalAddr().String())) // A points at relay
	resolver.put(makeRecord(t, relay.id, relay.t.LocalAddr().String()))

	router := newFakeRouter()
	router.put(a.id.Public().DestinationHash(), a.t.LocalAddr())
	router.put(b.id.Public().DestinationHash(), b.t.LocalAddr())
	relay.svc.SetRouter(router)

	incoming := make(chan *signaling.Channel, 1)
	b.svc.SetHandler(func(_ identity.Hash, ch *signaling.Channel) { incoming <- ch })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
		t.Fatal("B never accepted (relay didn't forward)")
	}
	defer chB.Close()

	if err := chA.Send(ctx, []byte("relayed payload")); err != nil {
		t.Fatal(err)
	}
	got, err := chB.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "relayed payload" {
		t.Fatalf("payload corrupted: %q", got)
	}
}

// TestRelay_HopLimit verifies envelopes with Hops >= MaxHops are dropped
// instead of forwarded.
//
// Strategy: send the hop-limited envelope, then a normal one. The fake
// router publishes every NextHop call to a channel; we wait for the
// normal envelope's call. By that point the dispatcher has finished with
// the prior packet (Inbox is FIFO, single-goroutine drain). If only one
// call arrives — limited dropped, normal forwarded — the contract holds.
func TestRelay_HopLimit(t *testing.T) {
	t.Parallel()
	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	relay := makePeer(t, hub, resolver)

	target, _ := identity.Generate(rand.Reader)
	targetHash := target.Public().DestinationHash()

	// Route to a black hole — an unregistered MemoryAddr. transport.Send
	// returns ErrUnknownPeer; no relay loop, just one NextHop call per
	// forwarded envelope.
	blackhole, _ := transport.ParseMemoryAddr("mem:blackhole")
	router := newFakeRouter()
	router.put(targetHash, blackhole)
	relay.svc.SetRouter(router)

	src := hub.NewMemoryTransport()
	t.Cleanup(func() { _ = src.Close() })

	send := func(hops byte, tag string) {
		env := &signaling.Envelope{
			Recipient: targetHash,
			Sender:    targetHash,
			SessionID: signaling.NewSessionID(),
			Hops:      hops,
			InnerType: signaling.InnerData,
			Payload:   []byte(tag),
		}
		blob, err := env.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := src.Send(context.Background(), relay.t.LocalAddr(), blob); err != nil {
			t.Fatal(err)
		}
	}

	// First: hop-limited (must be dropped).
	send(signaling.MaxHops, "limited")
	// Second: normal (must be forwarded). Its NextHop call signals that the
	// dispatcher has cleared the limited packet too — Inbox is FIFO.
	send(0, "normal")

	select {
	case <-router.calls:
		// One call observed: the normal one. Drain to ensure no second arrives.
	case <-time.After(2 * time.Second):
		t.Fatal("normal envelope was never forwarded")
	}
	select {
	case h := <-router.calls:
		t.Fatalf("hop-limited envelope was forwarded (extra call for %x)", h)
	default:
	}
}
