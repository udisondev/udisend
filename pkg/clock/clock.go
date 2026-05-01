// Package clock provides a Clock interface so timing-sensitive code can be
// tested deterministically. Production code uses Real, which forwards to
// time.Now and time.NewTimer; tests use a Fake whose Advance method moves
// the virtual clock forward and fires due timers in order.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock is the time source used throughout the project.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is a tiny abstraction over time.Timer to allow fake implementations.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// Real is the production clock. The zero value is valid.
type Real struct{}

// Now returns time.Now in UTC.
func (Real) Now() time.Time { return time.Now() }

// After forwards to time.After.
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// NewTimer wraps time.NewTimer.
func (Real) NewTimer(d time.Duration) Timer { return &realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time     { return r.t.C }
func (r *realTimer) Stop() bool              { return r.t.Stop() }
func (r *realTimer) Reset(d time.Duration) b { return r.t.Reset(d) }

type b = bool

// Fake is a deterministic Clock for tests.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// NewFake returns a Fake clock starting at t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

// Now returns the current virtual time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After returns a channel that fires after d virtual time has been advanced.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	return f.NewTimer(d).C()
}

// NewTimer creates a new fake timer.
func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{
		fires:  f.now.Add(d),
		c:      make(chan time.Time, 1),
		parent: f,
	}
	f.timers = append(f.timers, t)
	return t
}

// Advance moves the virtual clock forward by d and synchronously fires any
// timers whose deadline has passed.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	due := make([]*fakeTimer, 0)
	remaining := f.timers[:0]
	for _, t := range f.timers {
		if !t.fires.After(f.now) && !t.fired {
			t.fired = true
			due = append(due, t)
		} else if !t.fired {
			remaining = append(remaining, t)
		}
	}
	f.timers = remaining
	now := f.now
	f.mu.Unlock()

	sort.Slice(due, func(i, j int) bool { return due[i].fires.Before(due[j].fires) })
	for _, t := range due {
		select {
		case t.c <- now:
		default:
		}
	}
}

type fakeTimer struct {
	fires  time.Time
	c      chan time.Time
	parent *Fake
	fired  bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	t.parent.mu.Lock()
	defer t.parent.mu.Unlock()
	already := t.fired
	t.fired = true
	return !already
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.parent.mu.Lock()
	defer t.parent.mu.Unlock()
	wasActive := !t.fired
	t.fired = false
	t.fires = t.parent.now.Add(d)
	t.parent.timers = append(t.parent.timers, t)
	return wasActive
}
