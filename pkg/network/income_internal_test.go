package network

import (
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
)

// TestIncome_ReleaseZeroesFields — Release MUST clear all fields so a
// stale pool wrapper cannot leak peer data into a future Income event
// for an unrelated session.
func TestIncome_ReleaseZeroesFields(t *testing.T) {
	t.Parallel()

	inc := newIncome()
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

	inc := newIncome()
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
