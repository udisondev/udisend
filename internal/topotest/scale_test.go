package topotest_test

import (
	"context"
	mathrand "math/rand/v2"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/topotest"
	"github.com/udisondev/udisend/pkg/identity"
)

// §9 — scale tier. We exercise medium-sized clusters under
// `go test`. The 500-peer case is deliberately not the default; it
// runs only under `go test -tags=slow` to keep the regular suite
// fast.
//
// Scaling shape: build the network via Kademlia-natural bootstrap
// (every peer bootstraps against peer 0), then sample lookups
// rather than checking all-pairs (which is O(N²) and dominates the
// test). The sample size is fixed independent of N.

func runScale(t *testing.T, n int, lookupTimeout time.Duration) {
	t.Helper()

	// Scale tests run sequentially (no t.Parallel) so they do not
	// pile concurrent bootstraps onto the shared scheduler.
	c := topotest.New(topotest.Options{
		RequestTimeout: 5 * time.Second,
		LookupTimeout:  lookupTimeout,
	})
	c.Spawn(t, n)

	r := mathrand.New(mathrand.NewPCG(0xDEAD, 0xBEEF))

	// Bootstrap layout — multi-seed, like real deployments:
	//   - peers 0..seedCount-1 are seeds.
	//   - seeds form a small mesh first, so any of them is a viable
	//     entry point.
	//   - peers seedCount..n-1 each bootstrap against a random seed.
	//
	// Funnelling all N peers through one seed turns it into a
	// pathological hot spot: its recv loop is single-threaded, its
	// MemoryTransport buffer is fixed-size (256), and routing-table
	// mutex contention grows with N. Real Kademlia clients consult a
	// bootstrap LIST and pick from it, which is what we model here.
	//
	// seedCount scales with N: we cap each seed at ~20 incoming
	// bootstraps so its recv loop stays under load it can clear in
	// budget. For N=100 → 5 seeds, N=500 → 25, N=1000 → 50.
	seedCount := max(5, n/20)
	if n <= seedCount {
		// Tiny network — fall back to single-seed.
		for i := 1; i < n; i++ {
			ctx, cancel := context.WithTimeout(t.Context(), lookupTimeout)
			err := c.Peer(i).Node().Bootstrap(ctx, c.Peer(0).Transport().LocalAddr())
			cancel()
			if err != nil {
				t.Fatalf("bootstrap peer %d: %v", i, err)
			}
		}
	} else {
		// Seed mesh: every seed pings every other seed.
		for i := range seedCount {
			for j := i + 1; j < seedCount; j++ {
				c.Connect(t, i, j)
			}
		}
		// Each non-seed peer bootstraps against a random seed.
		for i := seedCount; i < n; i++ {
			seed := r.IntN(seedCount)
			ctx, cancel := context.WithTimeout(t.Context(), lookupTimeout)
			err := c.Peer(i).Node().Bootstrap(ctx, c.Peer(seed).Transport().LocalAddr())
			cancel()
			if err != nil {
				t.Fatalf("bootstrap peer %d (seed %d): %v", i, seed, err)
			}
		}
	}

	// One bucket-refresh pass per peer: each peer looks up a random
	// target, which forces iterativeFind to populate buckets that
	// the linear bootstrap may have left thin. Without this, peers
	// joined late only know their seed; with it, they know the
	// neighbourhood that seed walked them through.
	for i := range n {
		var rnd identity.Hash
		for k := range rnd {
			rnd[k] = byte(r.IntN(256))
		}
		ctx, cancel := context.WithTimeout(t.Context(), lookupTimeout)
		_, _ = c.Peer(i).Node().LookupNode(ctx, rnd)
		cancel()
	}
	// Sample 20 random pairs and assert the per-pair lookup success
	// rate. We do NOT require 100% — Kademlia with bucket size K=20
	// and a single bootstrap seed converges probabilistically: peers
	// that join after the first K saturate one bucket on the seed and
	// stop being visible to old peers without a periodic full refresh
	// (the MVP does not implement that). One warmup lookup per peer
	// fills most of the gaps, but the tail under -race instrumentation
	// can still show occasional misses. The ≥ 90% threshold flags real
	// regressions (drops below the noise floor) without fighting
	// steady-state behaviour.
	const (
		samples        = 20
		minSuccessFrac = 0.90
	)
	hits := 0
	for k := range samples {
		i := r.IntN(n)
		j := r.IntN(n)
		if i == j {
			j = (j + 1) % n
		}
		ctx, cancel := context.WithTimeout(t.Context(), lookupTimeout)
		closest, err := c.Peer(i).Node().LookupNode(ctx, c.Peer(j).ID())
		cancel()
		if err != nil {
			t.Fatalf("sample %d (peer %d → peer %d): %v", k, i, j, err)
		}
		target := c.Peer(j).ID()
		for _, ct := range closest {
			if ct.ID == target {
				hits++
				break
			}
		}
	}
	rate := float64(hits) / float64(samples)
	if rate < minSuccessFrac {
		t.Fatalf("scale N=%d: lookup success rate %.0f%% below %.0f%% threshold (%d/%d hits)",
			n, rate*100, minSuccessFrac*100, hits, samples)
	}
	t.Logf("scale N=%d: success rate %.0f%% (%d/%d)", n, rate*100, hits, samples)
}

func TestScale_N10(t *testing.T) {
	runScale(t, 10, 4*time.Second)
}

func TestScale_N50(t *testing.T) {
	runScale(t, 50, 8*time.Second)
}

// N=100 lives in scale_slow_test.go: under `-race` the detector's
// per-RTT latency overhead occasionally pushes a single PING past
// the 5 s budget when the seed is fielding 60+ in-flight probes,
// which makes the test mildly flaky in the default suite. Run
// `go test -tags=slow ./internal/topotest/...` to include it.
