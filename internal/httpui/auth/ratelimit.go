package auth

import (
	"sync"
	"time"
)

// RateLimiter is a per-IP sliding-window limiter for /login plus a
// cumulative-failure lockout. Two thresholds compose:
//
//   - PerMinute caps how often a single IP may attempt the form within
//     a 60-second window, regardless of outcome. Defends against trivial
//     scripted brute force.
//   - FailsToLock counts only verified-failure outcomes. After it crosses,
//     the IP is locked for LockDuration so even one-attempt-per-minute
//     pacing eventually halts.
//
// All state is in-memory. Cleanup is the caller's responsibility (call
// Cleanup periodically); state for an IP that hasn't attempted in a long
// time can be safely forgotten.
//
// MaxTrackedIPs hard-caps the in-memory map. When the cap is hit, new
// IPs are REFUSED (fail-closed) until Cleanup drains stale rows. The
// previous "transient state, no persistence" mode opened a bypass: an
// attacker who saturated the map (XFF spoofing, IPv6 /64 churn) could
// then hammer /login from any other fresh IP without ever accumulating
// failure counters — unlimited brute force. Failing-closed is correct
// here: an attacker keeping the table full holds open the front door
// for nobody, themselves included, and Cleanup runs hourly.
type RateLimiter struct {
	PerMinute     int
	FailsToLock   int
	LockDuration  time.Duration
	MaxTrackedIPs int
	Now           func() time.Time

	mu    sync.Mutex
	state map[string]*ipState
}

type ipState struct {
	attempts  []time.Time
	fails     int
	lockUntil time.Time
}

// Allow records an attempt for ip and returns whether it should proceed.
// On deny, retryAfter hints at how long until a retry might succeed (used
// for the Retry-After response header). The function combines the
// per-minute window check, the lockout check, and the fail-closed branch
// for a saturated tracking map.
func (l *RateLimiter) Allow(ip string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.ensure(ip)
	if st == nil {
		// Map saturated and ip is unknown — fail closed. Retry-After
		// nudges the caller toward the next Cleanup tick.
		return false, time.Hour
	}

	if now.Before(st.lockUntil) {
		return false, st.lockUntil.Sub(now)
	}

	cutoff := now.Add(-time.Minute)
	st.attempts = trimBefore(st.attempts, cutoff)
	if len(st.attempts) >= l.PerMinute {
		oldest := st.attempts[0]

		return false, oldest.Add(time.Minute).Sub(now)
	}
	st.attempts = append(st.attempts, now)

	return true, 0
}

// RecordFail bumps the failure counter for ip. When the counter reaches
// FailsToLock, the IP is locked for LockDuration. Crucially, the counter
// is NOT reset on lockout — only RecordSuccess clears it. That way an
// attacker waiting out a lockout cannot cycle through fresh per-minute
// windows: every additional failure past the threshold extends the
// lockout. Without this, the asymptotic brute-force budget collapses to
// FailsToLock per LockDuration (≈1900/day at default settings).
//
// No-op when the map is saturated and ip is unknown — Allow already
// refused the request, so there is nothing to attribute.
func (l *RateLimiter) RecordFail(ip string) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.ensure(ip)
	if st == nil {
		return
	}
	st.fails++
	if st.fails >= l.FailsToLock {
		st.lockUntil = now.Add(l.LockDuration)
		st.attempts = nil
	}
}

// RecordSuccess resets the failure counter and any lockout for ip.
// No-op when the map is saturated and ip is unknown.
func (l *RateLimiter) RecordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.ensure(ip)
	if st == nil {
		return
	}
	st.fails = 0
	st.lockUntil = time.Time{}
}

// Cleanup forgets state for IPs that have no recent attempts and no
// active lockout. Safe to call from a goroutine on a timer.
func (l *RateLimiter) Cleanup() {
	now := l.now()
	cutoff := now.Add(-time.Hour)
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, st := range l.state {
		if !now.Before(st.lockUntil) && (len(st.attempts) == 0 || st.attempts[len(st.attempts)-1].Before(cutoff)) {
			delete(l.state, ip)
		}
	}
}

// tracked is exposed only via _test files (lowercase) for assertions.
func (l *RateLimiter) tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.state)
}

// ensure returns the per-IP state, creating it if necessary. Returns nil
// when the map is saturated AND ip is not already tracked — callers
// must treat nil as "fail-closed: refuse without persisting state".
func (l *RateLimiter) ensure(ip string) *ipState {
	if l.state == nil {
		l.state = make(map[string]*ipState)
	}
	if st, ok := l.state[ip]; ok {
		return st
	}
	if l.MaxTrackedIPs > 0 && len(l.state) >= l.MaxTrackedIPs {
		return nil
	}
	st := &ipState{}
	l.state[ip] = st

	return st
}

func (l *RateLimiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}

	return time.Now()
}

func trimBefore(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}

	return ts[i:]
}
