// Package ratelimit provides a tiny per-key token-bucket limiter used by
// DHT and signaling layers to dampen DoS storms. One bucket per "key"
// (typically a source IP); each Allow consumes a token or returns false.
// Buckets refill at `rate` tokens per second and cap at `burst`. Idle
// keys older than 5 minutes are forgotten so attackers can't grow the
// map without bound.
package ratelimit

import (
	"sync"
	"time"
)

// DefaultMaxKeys hard-caps the number of distinct buckets a Limiter
// holds. Without a cap, the map grows unbounded between GC sweeps under
// IP-spoofing floods (1M unique sources/30s = ~96 MB of bucket structs
// alone). 100K is high enough for realistic legitimate traffic (one
// public-IP message-relay) and low enough to keep DoS bounded.
const DefaultMaxKeys = 100_000

// Limiter is a token-bucket per key with periodic GC of idle entries.
// Safe for concurrent use.
type Limiter struct {
	rate  float64       // tokens per second
	burst float64       // max bucket capacity
	idle  time.Duration // forget keys idle longer than this

	mu      sync.Mutex
	buckets map[string]*bucket
	clock   func() time.Time
	lastGC  time.Time // amortise gc — see Allow
	gcEvery time.Duration

	// MaxKeys caps the bucket map. Zero means DefaultMaxKeys; negative
	// disables capping (testing only). When the cap is hit and a fresh
	// key arrives, the bucket with the oldest lastSeen is evicted —
	// attackers spraying churn lose their own buckets first.
	MaxKeys int
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// New returns a Limiter with the given refill rate and burst capacity.
// rate <= 0 disables limiting (Allow always returns true). burst < 1 is
// rounded up to 1.
func New(rate, burst float64) *Limiter {
	return NewWithClock(rate, burst, time.Now)
}

// NewWithClock is New with an injected clock for deterministic tests.
func NewWithClock(rate, burst float64, clock func() time.Time) *Limiter {
	if burst < 1 {
		burst = 1
	}
	if clock == nil {
		clock = time.Now
	}

	return &Limiter{
		rate:    rate,
		burst:   burst,
		idle:    5 * time.Minute,
		gcEvery: 30 * time.Second,
		buckets: make(map[string]*bucket),
		clock:   clock,
	}
}

// Allow consumes one token for `key` and returns true if available. With
// a non-positive rate it always returns true. GC of idle buckets is
// amortised — at most once per gcEvery (default 30s) — so the hot path
// stays O(1) regardless of how many keys the limiter has seen.
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.rate <= 0 {
		return true
	}

	now := l.clock()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		l.evictIfFullLocked()
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = b
	}

	elapsed := now.Sub(b.lastSeen).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
	b.lastSeen = now

	l.maybeGC(now)

	if b.tokens < 1 {
		return false
	}
	b.tokens--

	return true
}

// evictIfFullLocked drops the least-recently-seen bucket when the map
// has reached MaxKeys. Caller MUST hold l.mu. Linear scan is O(MaxKeys);
// the cap is bounded so the cost is microseconds even when fully loaded.
func (l *Limiter) evictIfFullLocked() {
	limit := l.MaxKeys
	if limit == 0 {
		limit = DefaultMaxKeys
	}
	if limit < 0 || len(l.buckets) < limit {
		return
	}

	var (
		victim string
		oldest time.Time
		set    bool
	)
	for k, b := range l.buckets {
		if !set || b.lastSeen.Before(oldest) {
			victim = k
			oldest = b.lastSeen
			set = true
		}
	}
	if set {
		delete(l.buckets, victim)
	}
}

// maybeGC runs the idle-bucket sweep at most once per l.gcEvery. Caller
// must hold l.mu.
func (l *Limiter) maybeGC(now time.Time) {
	if l.gcEvery <= 0 {
		return
	}
	if !l.lastGC.IsZero() && now.Sub(l.lastGC) < l.gcEvery {
		return
	}
	l.lastGC = now

	cutoff := now.Add(-l.idle)
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}

// Size reports how many keys have an active bucket — useful for tests.
func (l *Limiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
