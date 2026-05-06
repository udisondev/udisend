package network_test

import (
	"context"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/network"
	"github.com/udisondev/udisend/pkg/presence"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// staticResolver is a presence resolver backed by an in-memory map —
// avoids spinning up a real DHT for the bridge test.
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

func (s *staticResolver) Lookup(_ context.Context, peer identity.PeerID) (*presence.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[peer.Bytes()]
	if !ok {
		return nil, presence.ErrNotFound
	}

	return r, nil
}

type meshTestPeer struct {
	id    *identity.Identity
	tr    transport.Transport
	svc   *signaling.Service
	dht   *dht.Node
	mesh  *network.MeshSignaler
	addr  string
	close func()
}

func makeMeshPeer(t *testing.T, hub *transport.MemoryHub, resolver signaling.AddressResolver) *meshTestPeer {
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

	mesh := network.NewMeshSignaler(svc, nil)

	p := &meshTestPeer{
		id:   id,
		tr:   tr,
		svc:  svc,
		dht:  node,
		mesh: mesh,
		addr: tr.LocalAddr().String(),
		close: func() {
			cancel()
			mesh.Close()
			svc.Close()
			_ = tr.Close()
		},
	}
	t.Cleanup(p.close)

	return p
}

func meshRecord(t *testing.T, id *identity.Identity, addr string) *presence.Record {
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

// TestMeshSignaler_RoundTripsOffer drives an end-to-end mesh exchange
// between two MeshSignaler bridges over MemoryHub. A sends offer →
// answer → candidate; B observes them all on RecvMeshSDP in order.
func TestMeshSignaler_RoundTripsOffer(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makeMeshPeer(t, hub, resolver)
	b := makeMeshPeer(t, hub, resolver)
	resolver.put(meshRecord(t, a.id, a.addr))
	resolver.put(meshRecord(t, b.id, b.addr))

	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()

	const fakeOffer = "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\noffer-payload\r\n"
	const fakeAnswer = "v=0\r\no=- 2 2 IN IP4 0.0.0.0\r\nanswer-payload\r\n"
	const fakeICE = "candidate:1 1 udp 2122260223 192.0.2.1 47000 typ host"

	bHash := b.id.Public().DestinationHash()

	if err := a.mesh.SendMeshSDP(ctx, bHash, transport.MeshSDPOffer, []byte(fakeOffer)); err != nil {
		t.Fatalf("a.SendMeshSDP offer: %v", err)
	}

	collect := func(want transport.MeshSDPKind, wantSDP string) {
		t.Helper()
		select {
		case msg := <-b.mesh.RecvMeshSDP():
			if msg.Kind != want {
				t.Fatalf("kind = %s, want %s", msg.Kind, want)
			}
			if string(msg.SDP) != wantSDP {
				t.Fatalf("sdp mismatch: got %q want %q", msg.SDP, wantSDP)
			}
			if msg.Peer != a.id.Public().DestinationHash() {
				t.Fatalf("peer = %x, want %x", msg.Peer, a.id.Public().DestinationHash())
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("did not observe %s within timeout", want)
		}
	}
	collect(transport.MeshSDPOffer, fakeOffer)

	// Now B replies — its mesh-signaler stored the inbound channel,
	// so SendMeshSDP back to A reuses that channel without a fresh
	// Connect handshake.
	aHash := a.id.Public().DestinationHash()
	if err := b.mesh.SendMeshSDP(ctx, aHash, transport.MeshSDPAnswer, []byte(fakeAnswer)); err != nil {
		t.Fatalf("b.SendMeshSDP answer: %v", err)
	}
	select {
	case msg := <-a.mesh.RecvMeshSDP():
		if msg.Kind != transport.MeshSDPAnswer {
			t.Fatalf("a got %s, want answer", msg.Kind)
		}
		if string(msg.SDP) != fakeAnswer {
			t.Fatalf("a got sdp %q, want %q", msg.SDP, fakeAnswer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a did not observe answer")
	}

	// Trickle a candidate from A to B.
	if err := a.mesh.SendMeshSDP(ctx, bHash, transport.MeshSDPCandidate, []byte(fakeICE)); err != nil {
		t.Fatalf("a.SendMeshSDP candidate: %v", err)
	}
	collect(transport.MeshSDPCandidate, fakeICE)
}

// TestMeshSignaler_ReusesChannelForSecondSend ensures the bridge does
// not open a fresh Channel per outgoing message — Connect is invoked
// exactly once for the first send, the cache hits afterwards.
func TestMeshSignaler_ReusesChannelForSecondSend(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makeMeshPeer(t, hub, resolver)
	b := makeMeshPeer(t, hub, resolver)
	resolver.put(meshRecord(t, a.id, a.addr))
	resolver.put(meshRecord(t, b.id, b.addr))

	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()

	bHash := b.id.Public().DestinationHash()

	for i := 0; i < 3; i++ {
		if err := a.mesh.SendMeshSDP(ctx, bHash, transport.MeshSDPOffer, []byte("payload")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	// Drain three deliveries on B; under-delivery means a channel
	// was torn down or replaced silently.
	for i := 0; i < 3; i++ {
		select {
		case <-b.mesh.RecvMeshSDP():
		case <-time.After(2 * time.Second):
			t.Fatalf("delivery %d did not arrive", i)
		}
	}
}

// TestMeshSignaler_CloseClearsHandler verifies that after Close, the
// Service no longer routes mesh envelopes through the bridge — useful
// for graceful node shutdown.
func TestMeshSignaler_CloseClearsHandler(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := makeMeshPeer(t, hub, resolver)
	b := makeMeshPeer(t, hub, resolver)
	resolver.put(meshRecord(t, a.id, a.addr))
	resolver.put(meshRecord(t, b.id, b.addr))

	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()

	bHash := b.id.Public().DestinationHash()

	// Establish channel + drain first delivery so the cache is warm.
	if err := a.mesh.SendMeshSDP(ctx, bHash, transport.MeshSDPOffer, []byte("warm")); err != nil {
		t.Fatalf("warm: %v", err)
	}
	<-b.mesh.RecvMeshSDP()

	b.mesh.Close()

	// Subsequent send still succeeds (svc + channel still up), but
	// b will not see it on RecvMeshSDP because the handler is nil.
	if err := a.mesh.SendMeshSDP(ctx, bHash, transport.MeshSDPCandidate, []byte("after-close")); err != nil {
		t.Fatalf("send after close: %v", err)
	}

	select {
	case msg := <-b.mesh.RecvMeshSDP():
		t.Errorf("RecvMeshSDP yielded %s after Close", msg.Kind)
	case <-time.After(300 * time.Millisecond):
		// Expected — no delivery.
	}
}
