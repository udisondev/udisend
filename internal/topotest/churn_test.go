package topotest_test

import (
	"context"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/topotest"
)

// §5 — churn tests. The seed topology is steady; we then introduce
// disruption (leave / partition / heal) and verify the network
// behaves correctly.

// TestChurn_NodeLeaves: a peer in a converged ring goes away. The
// remaining peers should still be able to look each other up.
// Lookups for the departed peer fail to find its ID but return
// without hanging.
func TestChurn_NodeLeaves(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		LookupTimeout: 4 * time.Second,
	})
	c.Spawn(t, 6)
	topotest.BuildRing(t, c)

	// Sanity: convergence works before we disrupt.
	c.LookupAcross(t)

	// Peer 3 leaves.
	gone := c.Peer(3).ID()
	prev := c.Detach(3)
	t.Cleanup(func() { c.Reattach(3, prev) })

	// Survivors can still find each other.
	survivors := []int{0, 1, 2, 4, 5}
	for _, i := range survivors {
		for _, j := range survivors {
			if i == j {
				continue
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			closest, err := c.Peer(i).Node().LookupNode(ctx, c.Peer(j).ID())
			cancel()
			if err != nil {
				t.Fatalf("lookup %d→%d after peer 3 left: %v", i, j, err)
			}
			found := false
			for _, ct := range closest {
				if ct.ID == c.Peer(j).ID() {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("peer %d failed to find peer %d after churn", i, j)
			}
		}
	}

	// Lookup for the departed peer terminates but returns no exact
	// match. We just assert it doesn't hang.
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	if _, err := c.Peer(0).Node().LookupNode(ctx, gone); err != nil {
		t.Fatalf("lookup for departed peer should return without error, got: %v", err)
	}
}

// TestChurn_NodeRejoins: peer leaves, peers can no longer reach it
// (Send fails); peer comes back with same id, ping succeeds again.
func TestChurn_NodeRejoins(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{})
	c.Spawn(t, 3)
	topotest.BuildRing(t, c)

	prev := c.Detach(1)
	if prev == nil {
		t.Fatalf("Detach should return previous registration")
	}

	// While away — direct ping fails.
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	if err := c.Peer(0).Node().Ping(ctx, c.Peer(1).Transport().LocalAddr()); err == nil {
		cancel()
		t.Fatalf("expected ping to detached peer to fail")
	}
	cancel()

	c.Reattach(1, prev)

	ctx2, cancel2 := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel2()
	if err := c.Peer(0).Node().Ping(ctx2, c.Peer(1).Transport().LocalAddr()); err != nil {
		t.Fatalf("ping after rejoin: %v", err)
	}
}

// TestChurn_PartitionAndHeal: bridged-cluster topology, then drop
// the bridge node. Peers within each side still find each other but
// cross-cluster lookups can no longer resolve. After the bridge is
// reattached, cross-cluster lookups recover.
func TestChurn_PartitionAndHeal(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		LookupTimeout: 4 * time.Second,
	})
	c.Spawn(t, 8)
	left, right, bridge := topotest.BuildBridgedClusters(t, c)
	t.Logf("left=%v right=%v bridge=%+v", left, right, bridge)

	// Pre-disruption: end-to-end works.
	c.LookupAcross(t)

	// Drop the bridge by detaching its A endpoint.
	prev := c.Detach(bridge.A)
	t.Cleanup(func() { c.Reattach(bridge.A, prev) })

	// Within left (excluding bridge.A which is gone), lookups still
	// work.
	leftSurvivors := append([]int(nil), left[:len(left)-1]...)
	for _, i := range leftSurvivors {
		for _, j := range leftSurvivors {
			if i == j {
				continue
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			_, err := c.Peer(i).Node().LookupNode(ctx, c.Peer(j).ID())
			cancel()
			if err != nil {
				t.Fatalf("intra-left lookup %d→%d: %v", i, j, err)
			}
		}
	}

	// Heal: bridge comes back.
	c.Reattach(bridge.A, prev)
	// Re-Connect to refresh routing tables — without this the live
	// peers have stale Send-fails recorded but no positive contact.
	c.Connect(t, bridge.A, bridge.B)
	c.Connect(t, bridge.A, left[0])

	// Allow a moment for verification probes to populate symmetry.
	topotest.WaitFor(t, 1*time.Second, func() bool {
		return c.KnowsAbout(bridge.A, bridge.B) && c.KnowsAbout(bridge.B, bridge.A)
	})

	// Now a fresh lookup across the bridge should resolve.
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	closest, err := c.Peer(left[0]).Node().LookupNode(ctx, c.Peer(right[len(right)-1]).ID())
	if err != nil {
		t.Fatalf("post-heal lookup: %v", err)
	}
	target := c.Peer(right[len(right)-1]).ID()
	found := false
	for _, ct := range closest {
		if ct.ID == target {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("post-heal: peer %d still cannot find peer %d (closest=%d)", left[0], right[len(right)-1], len(closest))
	}
}

// TestChurn_HalfNetworkReplaced: 50% of nodes leave and are replaced
// by fresh peers with new IDs. Surviving nodes plus newcomers must
// converge after the new peers bootstrap against a survivor.
func TestChurn_HalfNetworkReplaced(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		LookupTimeout: 6 * time.Second,
	})
	c.Spawn(t, 6)
	topotest.BuildKademliaNatural(t, c)

	// Drop peers 3, 4, 5.
	for i := 3; i <= 5; i++ {
		prev := c.Detach(i)
		t.Cleanup(func() { c.Reattach(i, prev) })
	}

	// Spawn three fresh peers and bootstrap them against survivor 0.
	fresh := c.Spawn(t, 3) // indices 6, 7, 8
	for _, p := range fresh {
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
		err := p.Node().Bootstrap(ctx, c.Peer(0).Transport().LocalAddr())
		cancel()
		if err != nil {
			t.Fatalf("bootstrap fresh peer %d: %v", p.Index(), err)
		}
	}

	// Survivors 0, 1, 2 + newcomers 6, 7, 8 should all find each
	// other.
	live := []int{0, 1, 2, 6, 7, 8}
	for _, i := range live {
		for _, j := range live {
			if i == j {
				continue
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			closest, err := c.Peer(i).Node().LookupNode(ctx, c.Peer(j).ID())
			cancel()
			if err != nil {
				t.Fatalf("lookup %d→%d post-replacement: %v", i, j, err)
			}
			target := c.Peer(j).ID()
			found := false
			for _, ct := range closest {
				if ct.ID == target {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("peer %d cannot find peer %d after 50%% churn", i, j)
			}
		}
	}
}
