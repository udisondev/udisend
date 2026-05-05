package topotest_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/topotest"
	"github.com/udisondev/udisend/pkg/dht"
)

// §7 — lookup correctness. Properties of iterativeFind we expect to
// hold for any reasonable converged topology.

// TestLookup_NonExistentTerminates: looking up a target that no peer
// owns must complete within LookupTimeout instead of looping. The
// returned closest list does not contain the target's ID.
func TestLookup_NonExistentTerminates(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		LookupTimeout: 3 * time.Second,
	})
	c.Spawn(t, 6)
	topotest.BuildKademliaNatural(t, c)

	ghost := dht.NodeID{}
	for i := range ghost {
		ghost[i] = 0xCD
	}

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	start := time.Now()
	closest, err := c.Peer(2).Node().LookupNode(ctx, ghost)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("lookup terminated with error: %v", err)
	}
	if elapsed > 3500*time.Millisecond {
		t.Fatalf("lookup of non-existent target took %v", elapsed)
	}
	for _, ct := range closest {
		if ct.ID == ghost {
			t.Fatalf("ghost ID materialised in result: %x", ghost)
		}
	}
}

// TestLookup_ParallelSameTarget: many concurrent lookups for the
// same target from the same node all succeed and return consistent
// results. Catches races in the lookup state machine and pending-
// request map.
func TestLookup_ParallelSameTarget(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		LookupTimeout: 5 * time.Second,
	})
	c.Spawn(t, 6)
	topotest.BuildKademliaNatural(t, c)

	target := c.Peer(5).ID()

	const concurrency = 10
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	misses := make(chan struct{}, concurrency)

	for range concurrency {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			closest, err := c.Peer(0).Node().LookupNode(ctx, target)
			if err != nil {
				errs <- err
				return
			}
			found := false
			for _, ct := range closest {
				if ct.ID == target {
					found = true
					break
				}
			}
			if !found {
				misses <- struct{}{}
			}
		})
	}
	wg.Wait()
	close(errs)
	close(misses)

	for err := range errs {
		t.Fatalf("parallel lookup: %v", err)
	}
	missCount := 0
	for range misses {
		missCount++
	}
	if missCount != 0 {
		t.Fatalf("%d/%d parallel lookups failed to find target", missCount, concurrency)
	}
}

// TestLookup_HopDiscipline: in a converged Kademlia-natural network,
// LookupAcross should always succeed within a small worst-case
// duration. Sets a generous bound (proportional to LookupTimeout)
// and asserts we don't burn the whole budget — a smoke check that
// lookups are converging fast, not just barely.
func TestLookup_HopDiscipline(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		LookupTimeout: 4 * time.Second,
	})
	const N = 8
	c.Spawn(t, N)
	topotest.BuildKademliaNatural(t, c)

	worst := c.LookupAcross(t)
	// Sanity: converged in-memory network should resolve every pair
	// well under 1 second. Loose bound: 2 seconds.
	if worst > 2*time.Second {
		t.Fatalf("worst-case lookup %v exceeds 2s budget for N=%d converged ring", worst, N)
	}
	t.Logf("N=%d converged worst lookup = %v", N, worst)
}

// TestLookup_PartialTopologySuccessRate: in a thin chain (each peer
// only knows its immediate neighbour at start), the iterative
// lookup should still find any target. Asserts 100% rate over a
// sample.
func TestLookup_PartialTopologySuccessRate(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		LookupTimeout: 5 * time.Second,
	})
	const N = 6
	c.Spawn(t, N)
	topotest.BuildChain(t, c)

	targets := make([]int, N)
	for i := range targets {
		targets[i] = i
	}
	rate := c.LookupSuccessRate(t, 0, targets)
	if rate < 1.0 {
		t.Fatalf("chain lookup success rate from peer 0 = %.2f, want 1.0", rate)
	}
}
