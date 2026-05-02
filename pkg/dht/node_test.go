package dht_test

import (
	"context"
	"crypto/rand"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
)

type peer struct {
	node *dht.Node
	t    transport.Transport
	id   *identity.Identity
	stop context.CancelFunc
}

type harness struct {
	hub   *transport.MemoryHub
	peers []*peer
	wg    sync.WaitGroup
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{hub: transport.NewMemoryHub()}
}

func (h *harness) spawn(t *testing.T, seed io.Reader) *peer {
	t.Helper()
	id, err := identity.Generate(seed)
	if err != nil {
		t.Fatal(err)
	}
	mt := h.hub.NewMemoryTransport()
	node := dht.NewNode(id, mt, nil, dht.Config{
		RequestTimeout: 500 * time.Millisecond,
		LookupTimeout:  3 * time.Second,
	})
	ctx, cancel := context.WithCancel(t.Context())
	p := &peer{node: node, t: mt, id: id, stop: cancel}
	h.peers = append(h.peers, p)
	h.wg.Go(func() {
		node.Run(ctx)
	})
	t.Cleanup(func() {
		cancel()
		_ = mt.Close()
	})
	return p
}

func (h *harness) wait() {
	for _, p := range h.peers {
		p.stop()
	}
	for _, p := range h.peers {
		_ = p.t.Close()
	}
	h.wg.Wait()
}

func TestPing_RoundTrip(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	a := h.spawn(t, rand.Reader)
	b := h.spawn(t, rand.Reader)
	defer h.wait()

	if err := a.node.Ping(t.Context(), b.t.LocalAddr()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	// Both tables should now know each other.
	if a.node.Table().Size() == 0 {
		t.Errorf("a.table empty")
	}
	if b.node.Table().Size() == 0 {
		t.Errorf("b.table empty")
	}
}

func TestBootstrap_ConvergesSmallNetwork(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const N = 6
	peers := make([]*peer, N)
	for i := range N {
		peers[i] = h.spawn(t, rand.Reader)
	}
	defer h.wait()

	// Connect everyone to peer 0.
	root := peers[0]
	for i := 1; i < N; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		if err := peers[i].node.Bootstrap(ctx, root.t.LocalAddr()); err != nil {
			cancel()
			t.Fatalf("bootstrap %d: %v", i, err)
		}
		cancel()
	}

	// Now any peer should be able to look up any other peer's contact via
	// iterative FIND_NODE.
	for i, p := range peers {
		for j, q := range peers {
			if i == j {
				continue
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			closest, err := p.node.LookupNode(ctx, q.node.ID())
			cancel()
			if err != nil {
				t.Fatalf("lookup %d→%d: %v", i, j, err)
			}
			found := false
			for _, c := range closest {
				if c.ID == q.node.ID() {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("peer %d failed to find peer %d (got %d closest)", i, j, len(closest))
			}
		}
	}
}

func TestPutValue_AndLookup(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const N = 5
	peers := make([]*peer, N)
	for i := range N {
		peers[i] = h.spawn(t, rand.Reader)
	}
	defer h.wait()

	for i := 1; i < N; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		if err := peers[i].node.Bootstrap(ctx, peers[0].t.LocalAddr()); err != nil {
			cancel()
			t.Fatalf("bootstrap %d: %v", i, err)
		}
		cancel()
	}

	key := identity.Hash{}
	copy(key[:], []byte("hello-key-12345!"))
	value := []byte("the value")

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	if err := peers[2].node.PutValue(ctx, key, value); err != nil {
		cancel()
		t.Fatalf("PutValue: %v", err)
	}
	cancel()

	// Look up from a peer that didn't initiate the put.
	ctx, cancel = context.WithTimeout(t.Context(), 4*time.Second)
	got, _, err := peers[4].node.LookupValue(ctx, key)
	cancel()
	if err != nil {
		t.Fatalf("LookupValue: %v", err)
	}
	if string(got) != string(value) {
		t.Fatalf("got %q, want %q", got, value)
	}
}
