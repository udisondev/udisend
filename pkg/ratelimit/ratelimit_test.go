package ratelimit_test

import (
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/ratelimit"
)

// fakeClock lets tests advance time deterministically so we don't need
// time.Sleep to wait for token refill.
type fakeClock struct{ now time.Time }

func TestLimiter_BurstThenDeny(t *testing.T) {
	t.Parallel()
	l := ratelimit.New(10, 3)
	for i := range 3 {
		if !l.Allow("ip1") {
			t.Fatalf("call %d should pass (burst=3)", i)
		}
	}
	if l.Allow("ip1") {
		t.Fatal("4th call must be denied with empty bucket")
	}
}

func TestLimiter_RefillsOverTime(t *testing.T) {
	t.Parallel()
	// Drain at t0, then ask again after t+1s (rate=2/s should refill 2 tokens).
	clock := &fakeClock{now: time.Unix(0, 0)}
	l := ratelimit.NewWithClock(2, 2, clock.tick)

	for i := range 2 {
		if !l.Allow("ip1") {
			t.Fatalf("burst call %d denied", i)
		}
	}
	if l.Allow("ip1") {
		t.Fatal("expected empty bucket")
	}
	clock.advance(1 * time.Second)
	if !l.Allow("ip1") {
		t.Fatal("expected refilled token after 1s at 2 tok/s")
	}
}

func TestLimiter_ZeroRateAllowsAll(t *testing.T) {
	t.Parallel()
	l := ratelimit.New(0, 0)
	for range 1000 {
		if !l.Allow("ip1") {
			t.Fatal("rate=0 should never deny")
		}
	}
}

func TestLimiter_IndependentKeys(t *testing.T) {
	t.Parallel()
	l := ratelimit.New(1, 1)
	for _, key := range []string{"a", "b"} {
		if !l.Allow(key) {
			t.Fatalf("key %q denied — independent keys must each get their burst", key)
		}
	}
}

// TestLimiter_BoundedKeyCardinality_EvictsLRU covers the unbounded-map
// bug: an attacker spraying 1M unique source IPs in 30s creates 1M
// bucket entries before the GC sweep sees them. The cap MUST evict the
// least-recently-seen bucket on insert past MaxKeys.
func TestLimiter_BoundedKeyCardinality_EvictsLRU(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Unix(1700000000, 0)}
	l := ratelimit.NewWithClock(100, 10, clock.tick)
	l.MaxKeys = 4

	// Insert four distinct IPs at distinct times — last-seen ordering is
	// the insertion order.
	for i, ip := range []string{"a", "b", "c", "d"} {
		if !l.Allow(ip) {
			t.Fatalf("setup: %s denied", ip)
		}
		clock.advance(10 * time.Millisecond)
		_ = i
	}
	if got := l.Size(); got != 4 {
		t.Fatalf("size after fill = %d, want 4", got)
	}

	// A fifth IP should evict 'a' (oldest lastSeen).
	if !l.Allow("e") {
		t.Fatal("new IP denied (cap evict path)")
	}
	if got := l.Size(); got != 4 {
		t.Errorf("size after evict-and-insert = %d, want 4 (cap-bound)", got)
	}
}

func TestLimiter_NilSafe(t *testing.T) {
	t.Parallel()
	var l *ratelimit.Limiter
	if !l.Allow("anything") {
		t.Fatal("nil receiver must allow")
	}
}

func (c *fakeClock) tick() time.Time { return c.now }
func (c *fakeClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}
