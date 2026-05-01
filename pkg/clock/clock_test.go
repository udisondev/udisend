package clock_test

import (
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/clock"
)

func TestFake_NowMovesWithAdvance(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	c := clock.NewFake(start)
	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now = %v, want %v", got, start)
	}
	c.Advance(5 * time.Second)
	if got := c.Now(); !got.Equal(start.Add(5 * time.Second)) {
		t.Fatalf("after advance got %v", got)
	}
}

func TestFake_TimerFiresOnAdvance(t *testing.T) {
	t.Parallel()
	c := clock.NewFake(time.Unix(0, 0))
	timer := c.NewTimer(2 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("timer fired prematurely")
	default:
	}
	c.Advance(1 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("timer fired after only 1s of 2s")
	default:
	}
	c.Advance(1 * time.Second)
	select {
	case <-timer.C():
	case <-time.After(time.Second):
		t.Fatal("timer never fired")
	}
}

func TestReal_NowAdvances(t *testing.T) {
	t.Parallel()
	var c clock.Real
	a := c.Now()
	time.Sleep(time.Millisecond)
	b := c.Now()
	if !b.After(a) {
		t.Fatalf("Real clock should advance")
	}
}
