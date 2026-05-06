package clock_test

import (
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/clock"
)

func TestRealClock_NowIsRecent(t *testing.T) {
	t.Parallel()

	c := clock.Real()
	before := time.Now()
	got := c.Now()
	after := time.Now()

	if got.Before(before) || got.After(after.Add(time.Millisecond)) {
		t.Fatalf("Real().Now() = %v, expected in [%v, %v]", got, before, after)
	}
}

func TestRealClock_ReturnsUTC(t *testing.T) {
	t.Parallel()

	if loc := clock.Real().Now().Location(); loc != time.UTC {
		t.Fatalf("Real().Now().Location() = %v, want UTC — callers depend on UTC for wire-format issuedAt", loc)
	}
}

func TestFakeClock_NowReturnsConfiguredTime(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(want)

	if got := fc.Now(); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}
}

func TestFakeClock_AdvanceShiftsTime(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	fc.Advance(5 * time.Second)

	want := start.Add(5 * time.Second)
	if got := fc.Now(); !got.Equal(want) {
		t.Fatalf("after Advance(5s): Now() = %v, want %v", got, want)
	}
}

func TestFakeClock_SetReplacesTime(t *testing.T) {
	t.Parallel()

	fc := clock.NewFake(time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC))
	target := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	fc.Set(target)

	if got := fc.Now(); !got.Equal(target) {
		t.Fatalf("after Set: Now() = %v, want %v", got, target)
	}
}

func TestFakeClock_ConcurrentAccessRaceFree(t *testing.T) {
	t.Parallel()

	fc := clock.NewFake(time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC))
	done := make(chan struct{})
	go func() {
		for range 1000 {
			fc.Advance(time.Millisecond)
		}
		close(done)
	}()
	for range 1000 {
		_ = fc.Now()
	}
	<-done
}
