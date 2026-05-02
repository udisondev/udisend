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
