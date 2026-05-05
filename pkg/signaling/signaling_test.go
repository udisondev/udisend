package signaling_test

import (
	"context"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// staticResolver gives the signaling layer an explicit peer→record
// mapping without going through a real DHT, simplifying test setup.
type staticResolver struct {
	mu   sync.Mutex
	recs map[identity.Hash]*presence.Record
}

func (s *staticResolver) put(rec *presence.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recs == nil {
		s.recs = make(map[identity.Hash]*presence.Record)
	}
	s.recs[rec.DestinationHash()] = rec
}

func (s *staticResolver) Lookup(_ context.Context, peer identity.Hash) (*presence.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[peer]
	if !ok {
		return nil, presence.ErrNotFound
	}
	return r, nil
}

type sigPeer struct {
	id   *identity.Identity
	t    transport.Transport
	node *dht.Node
	svc  *signaling.Service
}

func makePeer(t *testing.T, hub *transport.MemoryHub, resolver signaling.AddressResolver) *sigPeer {
	t.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tr := hub.NewMemoryTransport()
	svc := signaling.NewService(signaling.Config{
		Identity:  id,
		Transport: tr,
		Resolver:  resolver,
	})
	node := dht.NewNode(id, tr, nil, dht.Config{
		ExtraHandler: svc.HandlePacket,
	})
	ctx, cancel := context.WithCancel(t.Context())
	go node.Run(ctx)
	t.Cleanup(func() {
		cancel()
		svc.Close()
		_ = tr.Close()
	})
	return &sigPeer{id: id, t: tr, node: node, svc: svc}
}

func makeRecord(t *testing.T, id *identity.Identity, addr string) *presence.Record {
	t.Helper()
	r := &presence.Record{
		Address:  addr,
		IssuedAt: time.Now().UTC(),
	}
	if err := r.Sign(id); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestService_ConnectExchange(t *testing.T) {
	t.Parallel()
	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makePeer(t, hub, resolver)
	b := makePeer(t, hub, resolver)
	resolver.put(makeRecord(t, a.id, a.t.LocalAddr().String()))
	resolver.put(makeRecord(t, b.id, b.t.LocalAddr().String()))

	type incoming struct {
		peer identity.Hash
		ch   *signaling.Channel
	}
	in := make(chan incoming, 1)
	b.svc.SetHandler(func(peer identity.Hash, ch *signaling.Channel) {
		in <- incoming{peer: peer, ch: ch}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	chA, err := a.svc.Connect(ctx, b.id.Public().DestinationHash())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer chA.Close()

	var chB *signaling.Channel
	select {
	case got := <-in:
		if got.peer != a.id.Public().DestinationHash() {
			t.Fatal("peer mismatch")
		}
		chB = got.ch
	case <-time.After(5 * time.Second):
		t.Fatal("incoming handler never fired")
	}
	defer chB.Close()

	// A → B
	if err := chA.Send(ctx, []byte("hello from A")); err != nil {
		t.Fatal(err)
	}
	got, err := chB.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello from A" {
		t.Fatalf("got %q", got)
	}
	// B → A
	if err := chB.Send(ctx, []byte("hi from B")); err != nil {
		t.Fatal(err)
	}
	got, err = chA.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi from B" {
		t.Fatalf("got %q", got)
	}
}

func TestService_ConnectFailsForUnknownPeer(t *testing.T) {
	t.Parallel()
	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makePeer(t, hub, resolver)

	ghost, _ := identity.Generate(rand.Reader)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if _, err := a.svc.Connect(ctx, ghost.Public().DestinationHash()); err == nil {
		t.Fatal("expected failure")
	}
}

func TestEnvelope_Roundtrip(t *testing.T) {
	t.Parallel()
	sid, err := signaling.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	env := &signaling.Envelope{
		Recipient: identity.Hash{1, 2, 3},
		Sender:    identity.Hash{4, 5, 6},
		SessionID: sid,
		InnerType: signaling.InnerData,
		Payload:   []byte("blob"),
	}
	blob, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := signaling.Decode(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got.Recipient != env.Recipient {
		t.Fatal("recipient")
	}
	if got.InnerType != env.InnerType {
		t.Fatal("inner type")
	}
	if string(got.Payload) != "blob" {
		t.Fatal("payload")
	}
}

func FuzzDecodeEnvelope(f *testing.F) {
	env := &signaling.Envelope{InnerType: signaling.InnerData, Payload: []byte("x")}
	if blob, err := env.Encode(); err == nil {
		f.Add(blob)
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = signaling.Decode(data)
	})
}
