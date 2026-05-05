package topotest_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/topotest"
	"github.com/udisondev/udisend/pkg/transport"
)

// §2 — bootstrap tests. Validate the shape of "I just came up, where
// is the network?" — the DHT-layer bootstrap is what the network/Node
// layer drives with cfg.Bootstrap addresses.

// TestBootstrap_ThunderingHerd has N peers bootstrap simultaneously
// against a single seed. After the dust settles every peer must be
// able to find every other peer. Catches races in the seed's
// pending-request map under concurrent FIND_NODE.
//
// N is intentionally moderate: a thundering herd of 30+ peers
// triggers O(N²) verification probes against an as-yet-unpopulated
// routing table on the seed, which is slow but already covered by
// scale tests under -tags=slow. 8 peers is enough to exercise
// concurrency without the quadratic blowup.
func TestBootstrap_ThunderingHerd(t *testing.T) {
	t.Parallel()

	const N = 8
	c := topotest.New(topotest.Options{
		LookupTimeout: 8 * time.Second,
	})
	c.Spawn(t, N+1) // peer 0 = seed

	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 1; i <= N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			if err := c.Peer(i).Node().Bootstrap(ctx, c.Peer(0).Transport().LocalAddr()); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("bootstrap during thundering herd: %v", err)
	}

	// All peers should find each other.
	c.LookupAcross(t)
}

// TestBootstrap_DeadSeed_TimesOut: bootstrapping to an address that
// nobody is listening on must fail with the request error within the
// configured budget — not hang forever.
func TestBootstrap_DeadSeed_TimesOut(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		RequestTimeout: 200 * time.Millisecond,
	})
	c.Spawn(t, 1)

	dead, _ := transport.ParseMemoryAddr("mem:does-not-exist")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := c.Peer(0).Node().Bootstrap(ctx, dead)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected error bootstrapping against dead seed")
	}
	// Should bail well within the parent context: the unknown-peer
	// case fails fast (Send returns ErrUnknownPeer / timeout).
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("bootstrap to dead seed took %v, expected fast fail", elapsed)
	}
}

// TestBootstrap_Recovery: bootstrap fails while seed is detached,
// succeeds once seed is reattached. Models "all bootstrap nodes are
// down at startup but come back later".
func TestBootstrap_Recovery(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		RequestTimeout: 200 * time.Millisecond,
	})
	c.Spawn(t, 2)

	prev := c.Detach(0)
	if prev == nil {
		t.Fatalf("Detach should return previous registration")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()
	if err := c.Peer(1).Node().Bootstrap(ctx, c.Peer(0).Transport().LocalAddr()); err == nil {
		t.Fatalf("expected error while seed is detached")
	}

	c.Reattach(0, prev)

	ctx2, cancel2 := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel2()
	if err := c.Peer(1).Node().Bootstrap(ctx2, c.Peer(0).Transport().LocalAddr()); err != nil {
		t.Fatalf("bootstrap after reattach: %v", err)
	}

	if !c.KnowsAbout(1, 0) {
		t.Fatalf("peer 1 should know peer 0 after recovered bootstrap")
	}
}

// TestBootstrap_FailoverBetweenSeeds: simulates the higher-layer
// behaviour ("try cfg.Bootstrap one by one") — if seed0 is dead, the
// caller falls through to seed1 and the bootstrap succeeds.
func TestBootstrap_FailoverBetweenSeeds(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		RequestTimeout: 200 * time.Millisecond,
	})
	c.Spawn(t, 3) // 0 = dead seed, 1 = live seed, 2 = client

	prev := c.Detach(0)
	t.Cleanup(func() { c.Reattach(0, prev) })

	// First attempt to dead seed fails.
	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	if err := c.Peer(2).Node().Bootstrap(ctx, c.Peer(0).Transport().LocalAddr()); err == nil {
		cancel()
		t.Fatalf("expected error against dead seed")
	}
	cancel()

	// Fallback succeeds.
	ctx2, cancel2 := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel2()
	if err := c.Peer(2).Node().Bootstrap(ctx2, c.Peer(1).Transport().LocalAddr()); err != nil {
		t.Fatalf("bootstrap fallback: %v", err)
	}
}

// TestBootstrap_PartialSeedSet: half the seeds in a "list" are dead,
// the live half is enough. Asserts the harness can reproduce the
// `network.Open` semantics ("walk seeds until one works") at the DHT
// level.
func TestBootstrap_PartialSeedSet(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		RequestTimeout: 200 * time.Millisecond,
	})
	c.Spawn(t, 6)

	// Detach seeds 0, 1, 2 — only 3, 4 remain alive as seeds.
	dead := []int{0, 1, 2}
	prevs := make(map[int]*transport.MemoryTransport)
	for _, i := range dead {
		prevs[i] = c.Detach(i)
	}
	t.Cleanup(func() {
		for i, p := range prevs {
			c.Reattach(i, p)
		}
	})

	// Peer 5 attempts each seed; the simulated higher-layer just
	// loops until one succeeds.
	seedsToTry := []int{0, 1, 2, 3, 4}
	var lastErr error
	connected := false
	for _, seed := range seedsToTry {
		ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
		err := c.Peer(5).Node().Bootstrap(ctx, c.Peer(seed).Transport().LocalAddr())
		cancel()
		if err == nil {
			connected = true
			break
		}
		lastErr = err
	}
	if !connected {
		t.Fatalf("could not bootstrap against any live seed; last err: %v", lastErr)
	}
}
