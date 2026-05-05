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
