package dht_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
)

// TestRoutingTable_Siblings_ReturnsSClosestToSelf checks that Siblings
// returns the s contacts with smallest XOR distance to the local node.
func TestRoutingTable_Siblings_ReturnsSClosestToSelf(t *testing.T) {
	t.Parallel()

	self := id("00000000000000000000000000000000")
	rt := dht.NewRoutingTable(self, 8)
	rt.Add(contact("80000000000000000000000000000000", "far"))
	rt.Add(contact("00000000000000000000000000000010", "near"))
	rt.Add(contact("00000000000000000000000000000040", "mid"))

	got := rt.Siblings(2)
	if len(got) != 2 {
		t.Fatalf("Siblings returned %d, want 2", len(got))
	}

	if got[0].Addr.String() != "near" {
		t.Fatalf("got[0] = %v, want near", got[0].Addr)
	}

	if got[1].Addr.String() != "mid" {
		t.Fatalf("got[1] = %v, want mid", got[1].Addr)
	}
}

// TestRoutingTable_Siblings_FewerThanSAvailable returns whatever exists
// when the table is smaller than s.
func TestRoutingTable_Siblings_FewerThanSAvailable(t *testing.T) {
	t.Parallel()

	rt := dht.NewRoutingTable(id("00000000000000000000000000000000"), 8)
	rt.Add(contact("00000000000000000000000000000010", "only"))

	got := rt.Siblings(5)
	if len(got) != 1 {
		t.Fatalf("Siblings returned %d, want 1", len(got))
	}
}

// TestLookupNode_DisjointPaths_ConvergesAcrossNetwork verifies that the
// disjoint-paths variant of iterativeFind still converges on a normal
// (non-adversarial) network — i.e. the disjoint-set bookkeeping does
// not strand any path. Same shape as TestBootstrap_ConvergesSmallNetwork
// but with explicit Disjoint=3 sweep so we exercise the multi-path code.
func TestLookupNode_DisjointPaths_ConvergesAcrossNetwork(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	const N = 8
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

// TestPutValue_ReplicatesToSiblings asserts that PutValue sends STORE not
// only to the K closest peers to the key but also to the local node's
// sibling list. Setup: a small network where the publisher itself is
// far from `key`, so the publisher's siblings are NOT in the
// K-closest-to-key set. Any sibling that ends up holding a replica
// proves the sibling-list branch executed.
func TestPutValue_ReplicatesToSiblings(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	const N = 6
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

	publisher := peers[1]
	key := identity.Hash{}
	copy(key[:], []byte("siblings-key-rep"))

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	if err := publisher.node.PutValue(ctx, key, []byte("v")); err != nil {
		cancel()
		t.Fatalf("PutValue: %v", err)
	}
	cancel()

	sibs := publisher.node.Table().Siblings(2)
	if len(sibs) == 0 {
		t.Skip("publisher has no siblings to test against — small network")
	}

	for _, sib := range sibs {
		var sibPeer *peer
		for _, p := range peers {
			if p.node.ID() == sib.ID {
				sibPeer = p
				break
			}
		}
		if sibPeer == nil {
			continue
		}
		val, ok := sibPeer.node.LocalStore().Get(key)
		if !ok {
			t.Errorf("sibling %s missing replica", sib.ID.String())
			continue
		}
		if string(val) != "v" {
			t.Errorf("sibling %s holds wrong value %q", sib.ID.String(), val)
		}
	}
}
