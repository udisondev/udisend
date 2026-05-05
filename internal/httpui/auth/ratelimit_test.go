package auth

import (
	"sync/atomic"
	"testing"
	"time"
)

func newTestLimiter(now *atomic.Int64) *RateLimiter {
	return &RateLimiter{
		PerMinute:    5,
		FailsToLock:  20,
		LockDuration: 15 * time.Minute,
		Now:          func() time.Time { return time.Unix(now.Load(), 0) },
	}
}

func TestRateLimiter_Allows_FirstFiveInWindow(t *testing.T) {
	t.Parallel()

	now := atomic.Int64{}
	now.Store(1700000000)
	l := newTestLimiter(&now)

	for i := range 5 {
		ok, _ := l.Allow("203.0.113.1")
		if !ok {
			t.Fatalf("attempt %d denied; expected within window", i+1)
		}
	}
	ok, retry := l.Allow("203.0.113.1")
	if ok {
		t.Errorf("6th attempt allowed; expected rate-limit")
	}
	if retry <= 0 {
		t.Errorf("retryAfter = %s, want > 0", retry)
	}
}

func TestRateLimiter_WindowSlides(t *testing.T) {
	t.Parallel()

	now := atomic.Int64{}
	now.Store(1700000000)
	l := newTestLimiter(&now)

	for range 5 {
		l.Allow("203.0.113.2")
	}
	if ok, _ := l.Allow("203.0.113.2"); ok {
		t.Fatal("setup: 6th attempt should be denied")
	}

	now.Add(61) // slide one minute forward
	if ok, _ := l.Allow("203.0.113.2"); !ok {
		t.Errorf("after window slid, attempt should be allowed")
	}
}

func TestRateLimiter_LockoutAfterFails(t *testing.T) {
	t.Parallel()

	now := atomic.Int64{}
	now.Store(1700000000)
	l := newTestLimiter(&now)

	for range 20 {
		l.RecordFail("203.0.113.3")
	}
	ok, retry := l.Allow("203.0.113.3")
	if ok {
		t.Errorf("locked IP allowed")
	}
	if retry < 14*time.Minute {
		t.Errorf("retryAfter = %s, want ≈ 15min", retry)
	}

	now.Add(int64(15 * time.Minute / time.Second))
	now.Add(1)
	if ok, _ := l.Allow("203.0.113.3"); !ok {
		t.Errorf("after lockout expired, IP should be allowed")
	}
}

func TestRateLimiter_FailsAccumulateAcrossLockouts(t *testing.T) {
	t.Parallel()

	now := atomic.Int64{}
	now.Store(1700000000)
	l := newTestLimiter(&now)

	for range 20 {
		l.RecordFail("203.0.113.30")
	}
	if ok, _ := l.Allow("203.0.113.30"); ok {
		t.Fatal("setup: expected first lockout")
	}

	// Advance past first lockout.
	now.Add(int64(15*time.Minute/time.Second) + 1)
	if ok, _ := l.Allow("203.0.113.30"); !ok {
		t.Fatal("setup: should be allowed after lockout expires")
	}

	// One more fail must immediately re-trigger lockout — counter was
	// not reset to zero, so this fail crosses the threshold again.
	l.RecordFail("203.0.113.30")
	if ok, retry := l.Allow("203.0.113.30"); ok {
		t.Errorf("post-lockout single fail did not re-arm; allow=true retry=%s", retry)
	}
}

func TestRateLimiter_RecordSuccessClears(t *testing.T) {
	t.Parallel()

	now := atomic.Int64{}
	now.Store(1700000000)
	l := newTestLimiter(&now)

	for range 19 {
		l.RecordFail("203.0.113.4")
	}
	l.RecordSuccess("203.0.113.4")

	// One more fail must NOT trip the lockout because counter was reset.
	l.RecordFail("203.0.113.4")
	if ok, _ := l.Allow("203.0.113.4"); !ok {
		t.Errorf("IP locked despite RecordSuccess having reset the counter")
	}
}

func TestRateLimiter_PerIPIsolation(t *testing.T) {
	t.Parallel()

	now := atomic.Int64{}
	now.Store(1700000000)
	l := newTestLimiter(&now)

	for range 5 {
		l.Allow("203.0.113.5")
	}
	if ok, _ := l.Allow("203.0.113.5"); ok {
		t.Fatal("setup: 6th from same IP should be denied")
	}

	if ok, _ := l.Allow("203.0.113.6"); !ok {
		t.Errorf("different IP affected by another IP's bucket")
	}
}

// TestRateLimiter_FailClosedWhenSaturated covers the bypass: once
// MaxTrackedIPs is hit, the previous behaviour returned a transient
// &ipState{} that swallowed RecordFail mutations. An attacker who fills
// the table with junk IPs (X-Forwarded-For spoofing, IPv6 /64 churn)
// could then hammer /login from a fresh IP without ever accumulating
// fails — unlimited brute force. The defence is fail-closed: refuse new
// IPs until Cleanup drains the cap.
func TestRateLimiter_FailClosedWhenSaturated(t *testing.T) {
	t.Parallel()

	now := atomic.Int64{}
	now.Store(1700000000)
	l := &RateLimiter{
		PerMinute:     5,
		FailsToLock:   20,
		LockDuration:  15 * time.Minute,
		MaxTrackedIPs: 4,
		Now:           func() time.Time { return time.Unix(now.Load(), 0) },
	}

	// Saturate the cap with four distinct IPs.
	for i, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4"} {
		if ok, _ := l.Allow(ip); !ok {
			t.Fatalf("setup: IP #%d %q denied; map cap not yet reached", i, ip)
		}
	}
	if l.tracked() != 4 {
		t.Fatalf("tracked = %d, want 4", l.tracked())
	}

	// A fresh IP arrives. With cap saturated the limiter MUST refuse
	// rather than evaluate the request against fresh-zero state.
	if ok, retry := l.Allow("198.51.100.5"); ok {
		t.Fatalf("over-cap IP allowed; want fail-closed; retry=%s", retry)
	}

	// Brute-force scenario: same fresh IP keeps being refused — the
	// counter is irrelevant because Allow already short-circuits.
	for range 10 {
		if ok, _ := l.Allow("198.51.100.5"); ok {
			t.Fatal("over-cap IP allowed on later attempt; bypass still present")
		}
	}

	// After Cleanup empties the cap, the previously-refused IP is
	// admitted normally — the protection is self-healing, not a permanent
	// shutout.
	now.Add(int64(2 * time.Hour / time.Second))
	l.Cleanup()
	if l.tracked() != 0 {
		t.Fatalf("Cleanup did not drain saturated state; tracked = %d", l.tracked())
	}
	if ok, _ := l.Allow("198.51.100.5"); !ok {
		t.Errorf("post-Cleanup admission denied; protection is supposed to self-heal")
	}
}

func TestRateLimiter_Cleanup(t *testing.T) {
	t.Parallel()

	now := atomic.Int64{}
	now.Store(1700000000)
	l := newTestLimiter(&now)

	l.Allow("203.0.113.7")
	if l.tracked() != 1 {
		t.Fatalf("tracked = %d, want 1", l.tracked())
	}

	now.Add(int64(2 * time.Hour / time.Second))
	l.Cleanup()
	if l.tracked() != 0 {
		t.Errorf("Cleanup left %d entries; expected 0 after long idle", l.tracked())
	}
}
