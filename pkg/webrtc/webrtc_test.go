package webrtc_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
	uwebrtc "github.com/udisondev/udisend/pkg/webrtc"
)

func TestSignedSDP_Roundtrip(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)
	signed := uwebrtc.SignedSDP{Kind: uwebrtc.SDPTypeOffer, SDP: "v=0\r\n..."}
	signed.Sign(id)
	blob, err := signed.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var got uwebrtc.SignedSDP
	if err := got.UnmarshalBinary(blob); err != nil {
		t.Fatal(err)
	}
	if got.SDP != signed.SDP {
		t.Fatal("sdp")
	}
	if err := got.Verify(id.Public()); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestSignedSDP_RejectsTamper(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)
	signed := uwebrtc.SignedSDP{Kind: uwebrtc.SDPTypeOffer, SDP: "fingerprint:abcd"}
	signed.Sign(id)
	signed.SDP = "fingerprint:DEAD" // post-sign tamper
	if err := signed.Verify(id.Public()); !errors.Is(err, uwebrtc.ErrSDPSignature) {
		t.Fatalf("err = %v, want ErrSDPSignature", err)
	}
}

// staticResolver lets the signaling layer resolve peer hashes without DHT.
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

type signalNode struct {
	id   *identity.Identity
	t    transport.Transport
	svc  *signaling.Service
	node *dht.Node
}

func mkSignalNode(t *testing.T, hub *transport.MemoryHub, resolver signaling.AddressResolver) *signalNode {
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
	node := dht.NewNode(id, tr, nil, dht.Config{ExtraHandler: svc.HandlePacket})
	ctx, cancel := context.WithCancel(t.Context())
	go node.Run(ctx)
	t.Cleanup(func() {
		cancel()
		svc.Close()
		_ = tr.Close()
	})
	return &signalNode{id: id, t: tr, svc: svc, node: node}
}

func mkRecord(t *testing.T, id *identity.Identity, addr string) *presence.Record {
	t.Helper()
	r := &presence.Record{Address: addr, IssuedAt: time.Now().UTC()}
	if err := r.Sign(id); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWebRTC_DataChannelExchange(t *testing.T) {
	t.Parallel()
	hub := transport.NewMemoryHub()
	resolver := &staticResolver{}
	a := mkSignalNode(t, hub, resolver)
	b := mkSignalNode(t, hub, resolver)
	resolver.put(mkRecord(t, a.id, a.t.LocalAddr().String()))
	resolver.put(mkRecord(t, b.id, b.t.LocalAddr().String()))

	incoming := make(chan *signaling.Channel, 1)
	b.svc.SetHandler(func(_ identity.Hash, ch *signaling.Channel) {
		incoming <- ch
	})

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	chA, err := a.svc.Connect(ctx, b.id.Public().DestinationHash())
	if err != nil {
		t.Fatalf("signal connect: %v", err)
	}
	t.Cleanup(func() { _ = chA.Close() })
	chB := <-incoming
	t.Cleanup(func() { _ = chB.Close() })

	sessA, err := uwebrtc.NewSession(uwebrtc.Config{
		Identity:   a.id,
		PeerPublic: b.id.Public(),
		Channel:    chA,
		Initiator:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessA.Close() })
	sessB, err := uwebrtc.NewSession(uwebrtc.Config{
		Identity:   b.id,
		PeerPublic: a.id.Public(),
		Channel:    chB,
		Initiator:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessB.Close() })

	gotMsg := make(chan []byte, 1)
	sessB.SetMessageHandler(func(p []byte) {
		select {
		case gotMsg <- p:
		default:
		}
	})

	var wg sync.WaitGroup
	wg.Add(2)
	var aErr, bErr error
	go func() { defer wg.Done(); aErr = sessA.Run(ctx) }()
	go func() { defer wg.Done(); bErr = sessB.Run(ctx) }()
	wg.Wait()
	if aErr != nil {
		t.Fatalf("initiator: %v", aErr)
	}
	if bErr != nil {
		t.Fatalf("responder: %v", bErr)
	}

	select {
	case <-sessA.DataReady():
	case <-time.After(10 * time.Second):
		t.Fatal("DataChannel A never opened")
	}
	select {
	case <-sessB.DataReady():
	case <-time.After(10 * time.Second):
		t.Fatal("DataChannel B never opened")
	}

	if err := sessA.SendData(ctx, []byte("hello over webrtc")); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case got := <-gotMsg:
		if string(got) != "hello over webrtc" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("message not delivered")
	}
}
