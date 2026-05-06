package network

import (
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
)

// localIncome builds an *Income tied to a test-local sync.Pool. Using
// the package-global incomePool here would race under -race + parallel
// tests: after Release the wrapper goes back to the global pool and a
// concurrent newIncome() call (from any other test creating a Node)
// can re-acquire it and write to the same memory we are about to read.
// A test-local pool keeps the asserted wrapper unreachable from other
// goroutines for the lifetime of the test.
func localIncome(t *testing.T) (*Income, *sync.Pool) {
	t.Helper()

	pool := &sync.Pool{New: func() any { return &Income{} }}
	inc := pool.Get().(*Income)
	inc.pool = pool

	return inc, pool
}

// TestIncome_ReleaseZeroesFields — Release MUST clear all fields so a
// stale pool wrapper cannot leak peer data into a future Income event
// for an unrelated session.
func TestIncome_ReleaseZeroesFields(t *testing.T) {
	t.Parallel()

	inc, _ := localIncome(t)
	inc.Peer = identity.Hash{1, 2, 3}
	inc.SessionID = SessionID{4, 5, 6}
	inc.Payload = []byte("dirty data")
	inc.Final = true

	inc.Release()

	if inc.Peer != (identity.Hash{}) {
		t.Errorf("Peer not zeroed: %x", inc.Peer)
	}
	if inc.SessionID != (SessionID{}) {
		t.Errorf("SessionID not zeroed: %x", inc.SessionID)
	}
	if inc.Payload != nil {
		t.Errorf("Payload not nil: %v", inc.Payload)
	}
	if inc.Final {
		t.Error("Final not reset")
	}
	if inc.pool != nil {
		t.Error("pool not nil — second Release would double-Put")
	}
}

// TestIncome_ReleaseIdempotent — defer Release after explicit Release
// on early-return paths must be safe.
func TestIncome_ReleaseIdempotent(t *testing.T) {
	t.Parallel()

	inc, _ := localIncome(t)
	inc.Release()
	inc.Release()
}

// TestIncome_AllocsAmortized — newIncome+Release in a tight loop must
// amortize to ≈0 allocs/op once the pool is warm. Without pooling this
// would be 1.0 alloc/op.
func TestIncome_AllocsAmortized(t *testing.T) {
	// Warm the pool.
	for range 100 {
		newIncome().Release()
	}

	allocs := testing.AllocsPerRun(1000, func() {
		inc := newIncome()
		inc.Release()
	})
	if allocs > 0.1 {
		t.Errorf("allocs/op = %v, want ≤0.1 (pool not effective)", allocs)
	}
}

// BenchmarkIncomePool measures pool overhead per get/release cycle.
func BenchmarkIncomePool(b *testing.B) {
	for b.Loop() {
		inc := newIncome()
		inc.Release()
	}
}

// TestIncomeChannel_CloseAfterConcurrentSenders — the production race
// reproducer: many goroutines spam Send while Close fires from a
// separate goroutine. Pre-fix this would either panic with "send on
// closed channel" (close racing past a sender already past its
// closed-flag check) or be flagged by the race detector. With the
// RWMutex coordination the panic is impossible: Close cannot
// acquire the write Lock until every in-flight Send releases its
// RLock.
func TestIncomeChannel_CloseAfterConcurrentSenders(t *testing.T) {
	t.Parallel()

	const senders = 32
	const sendsPerSender = 200

	c := newIncomeChannel(senders) // shallow buffer to force contention
	abort := make(chan struct{})
	var wg sync.WaitGroup

	// Drain consumer — pulls items so senders don't block forever.
	go func() {
		for inc := range c.Recv() {
			inc.Release()
		}
	}()

	wg.Add(senders)
	for range senders {
		go func() {
			defer wg.Done()
			for range sendsPerSender {
				inc, _ := localIncome(t)
				if !c.Send(inc, abort) {
					inc.Release()
				}
			}
		}()
	}

	// Close from a separate goroutine while senders are still active.
	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		c.Close()
	}()

	wg.Wait()
	<-closeDone

	// Sanity: a second Close MUST be a no-op (idempotent) and not panic
	// on double-close-of-channel.
	c.Close()

	// Sanity: post-Close Send returns false without panic and never
	// blocks waiting for the (closed) consumer.
	inc, _ := localIncome(t)
	if c.Send(inc, abort) {
		t.Fatal("Send after Close returned true")
	}
	inc.Release()
}

// TestIncomeChannel_AbortUnblocksFullBuffer — when the consumer has
// stopped reading and the buffer is full, a fresh Send must not pin
// the goroutine forever. Closing the abort channel makes Send return
// false promptly so callers (and Close) are not starved.
func TestIncomeChannel_AbortUnblocksFullBuffer(t *testing.T) {
	t.Parallel()

	c := newIncomeChannel(1)
	abort := make(chan struct{})

	first, _ := localIncome(t)
	if !c.Send(first, abort) {
		t.Fatal("first Send rejected unexpectedly")
	}

	// Buffer now full; second Send blocks in select until abort.
	done := make(chan bool, 1)
	go func() {
		second, _ := localIncome(t)
		ok := c.Send(second, abort)
		if !ok {
			second.Release()
		}
		done <- ok
	}()

	close(abort)

	// Wall-clock watchdog: this is the negative-case timeout — the
	// test fails if Send fails to unblock within 1s. Wall-clock is
	// normally a flake source in tests; using it as a hang-detector
	// (where the only outcome is "should be near-instant or test
	// fails") is OK and the only practical way to assert the
	// unblocking property without synctest.
	select {
	case ok := <-done:
		if ok {
			t.Fatal("Send returned true after abort")
		}
	case <-time.After(time.Second):
		t.Fatal("Send did not unblock after abort")
	}

	// Drain the first Send's payload so Close is not racing with
	// a pending receive (test cleanliness).
	<-c.Recv()
	first.Release()
	c.Close()
}
