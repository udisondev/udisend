// Package clock decouples wall-clock time from production code so tests
// can drive time deterministically.
//
// Use [Clock] in any function whose behaviour depends on the current
// time (TTL checks, signed-record IssuedAt, contact freshness, retry
// schedules). In production wire [Real] into the constructor; in tests
// substitute [Fake] and call [Fake.Advance] / [Fake.Set] to step time.
//
// All clocks return UTC so callers can use the result directly in
// signed records / wire-format encoders without per-site `.UTC()`
// conversions.
package clock

import (
	"sync"
	"time"
)

// Clock returns the current time. Implementations are safe for
// concurrent use.
type Clock interface {
	Now() time.Time
}

// Real returns a [Clock] backed by [time.Now]. The returned clock is a
// zero-cost shared singleton — multiple callers share state-free
// behaviour.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// FakeClock is a deterministic [Clock] for tests. It is safe for
// concurrent use across goroutines.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a [FakeClock] initialised to start.
func NewFake(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// Now reports the current fake time.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.now
}

// Advance moves the fake clock forward by d.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.now = f.now.Add(d)
}

// Set replaces the current fake time with t.
func (f *FakeClock) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.now = t
}
