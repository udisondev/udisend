package dht_test

import (
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
)

// TestMemoryStore_BoundedSize_EvictsNearestExpiry guards the unbounded-
// growth bug: an attacker (or a buggy peer) can call PUT with random
// keys until the map exhausts available memory. The store MUST cap its
// entry count and drop the entry with the nearest expiry when the cap
// is reached.
func TestMemoryStore_BoundedSize_EvictsNearestExpiry(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	s := dht.NewMemoryStore(clock)
	s.MaxEntries = 4

	mk := func(b byte) dht.NodeID {
		var id dht.NodeID
		id[0] = b
		return id
	}

	// Insert four entries with distinct TTLs (10s, 20s, 30s, 40s).
	for i, ttl := range []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 40 * time.Second} {
		s.Put(mk(byte(i+1)), []byte{byte(i + 1)}, ttl)
	}
	if got := s.Size(); got != 4 {
		t.Fatalf("size = %d, want 4", got)
	}

	// A fifth entry MUST evict the soonest-expiring entry (key=1, ttl=10s).
	s.Put(mk(5), []byte{5}, 50*time.Second)
	if got := s.Size(); got != 4 {
		t.Errorf("size after over-cap put = %d, want 4 (cap-bound)", got)
	}
	if _, ok := s.Get(mk(1)); ok {
		t.Errorf("nearest-expiry entry not evicted")
	}
	if _, ok := s.Get(mk(5)); !ok {
		t.Errorf("new entry not stored")
	}
}

// TestMemoryStore_BoundedSize_UpdateExistingDoesNotGrow confirms that
// Put on an already-known key never trips eviction — only fresh keys
// past the cap should evict.
func TestMemoryStore_BoundedSize_UpdateExistingDoesNotGrow(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	s := dht.NewMemoryStore(clock)
	s.MaxEntries = 2

	var k1, k2 dht.NodeID
	k1[0] = 1
	k2[0] = 2
	s.Put(k1, []byte("a"), time.Minute)
	s.Put(k2, []byte("b"), time.Minute)
	s.Put(k1, []byte("a-updated"), time.Minute) // overwrite, not new

	got, ok := s.Get(k1)
	if !ok || string(got) != "a-updated" {
		t.Errorf("k1 = %q, ok=%v; want 'a-updated', true", got, ok)
	}
	if _, ok := s.Get(k2); !ok {
		t.Errorf("k2 evicted by an in-place update")
	}
}
