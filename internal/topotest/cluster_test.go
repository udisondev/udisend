package topotest_test

import (
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/topotest"
)

// TestHarness_Connect_LearnsBothWays verifies the basic Connect
// primitive: after pinging j from i, peer i has j synchronously and
// peer j eventually has i once its verification probe completes.
func TestHarness_Connect_LearnsBothWays(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{})
	c.Spawn(t, 2)

	c.Connect(t, 0, 1)

	if !c.KnowsAbout(0, 1) {
		t.Fatalf("peer 0 should know peer 1 synchronously after Ping")
	}
	ok := topotest.WaitFor(t, 2*time.Second, func() bool {
		return c.KnowsAbout(1, 0)
	})
	if !ok {
		t.Fatalf("peer 1 never learned peer 0 through verification probe")
	}
}

// TestHarness_Detach_Reattach takes a peer off the hub, asserts that
// new packets to it fail, then reattaches and asserts the peer can
// be reached again. Foundational for partition tests.
func TestHarness_Detach_Reattach(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{})
	c.Spawn(t, 2)

	c.Connect(t, 0, 1)
	prev := c.Detach(1)
	if prev == nil {
		t.Fatalf("Detach should return the previous registration")
	}

	// While detached, peer 0 cannot ping peer 1 — Send fails because
	// the hub no longer maps the id.
	ctx, cancel := newTimeoutCtx(t, 500*time.Millisecond)
	defer cancel()
	if err := c.Peer(0).Node().Ping(ctx, c.Peer(1).Transport().LocalAddr()); err == nil {
		t.Fatalf("ping to detached peer should fail")
	}

	c.Reattach(1, prev)
	ctx2, cancel2 := newTimeoutCtx(t, 1*time.Second)
	defer cancel2()
	if err := c.Peer(0).Node().Ping(ctx2, c.Peer(1).Transport().LocalAddr()); err != nil {
		t.Fatalf("ping after reattach: %v", err)
	}
}

// TestHarness_Bootstrap_PullsNeighborhood sanity-checks that calling
// Bootstrap (not just Connect) populates the routing table beyond the
// single seed: peer 2, after bootstrapping against peer 0 in a
// pre-built ring, learns about more than just peer 0.
func TestHarness_Bootstrap_PullsNeighborhood(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{})
	c.Spawn(t, 5)

	// pre-seed a ring among 0..3 so peer 0 has multiple contacts to
	// hand out.
	c.Connect(t, 0, 1)
	c.Connect(t, 1, 2)
	c.Connect(t, 2, 3)
	c.Connect(t, 3, 0)

	// Wait for the back-edges (B learns A via probe) to settle so the
	// routing tables are populated symmetrically.
	topotest.WaitFor(t, 1*time.Second, func() bool {
		return c.AllPairsKnow([]int{0, 1, 2, 3})
	})

	// Now peer 4 bootstraps against peer 0. After that, peer 4 should
	// have multiple contacts (≥ 2), not just peer 0.
	c.Bootstrap(t, 4, 0)

	got := c.Peer(4).Node().Table().Size()
	if got < 2 {
		t.Fatalf("after Bootstrap(4→0) expected ≥ 2 contacts, got %d", got)
	}
}
