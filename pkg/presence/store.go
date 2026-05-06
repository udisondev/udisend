package presence

import (
	"sync"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
)

// Cache is an in-memory map of recently-seen presence records, keyed by
// destination hash. Older records for the same peer are replaced when a
// newer (later IssuedAt) record arrives.
type Cache struct {
	mu      sync.RWMutex
	records map[identity.Hash]*Record
	ttl     time.Duration
}

// NewCache returns an empty cache with the given TTL.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{records: make(map[identity.Hash]*Record), ttl: ttl}
}

// Put accepts r if its signature is valid and it is fresher than any
// already-stored record for the same peer. Returns true if accepted.
func (c *Cache) Put(now time.Time, r *Record) (bool, error) {
	if err := r.Verify(now, c.ttl); err != nil {
		return false, err
	}
	key := r.DestinationHash()
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.records[key]; ok {
		if !r.IssuedAt.After(existing.IssuedAt) {
			return false, nil
		}
	}
	cp := *r
	c.records[key] = &cp
	return true, nil
}

// Get fetches the most recent valid record for peer. Returns (nil,
// false) if not present or expired.
func (c *Cache) Get(now time.Time, peer identity.PeerID) (*Record, bool) {
	key := peer.Bytes()
	c.mu.RLock()
	r, ok := c.records[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if r.Expired(now, c.ttl) {
		return nil, false
	}
	cp := *r

	return &cp, true
}

// Sweep removes expired records.
func (c *Cache) Sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, r := range c.records {
		if r.Expired(now, c.ttl) {
			delete(c.records, k)
		}
	}
}

// Size reports the current cache size, mostly useful in tests.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.records)
}

// All returns a snapshot of every cached record. The returned slice is
// a copy; mutating it is safe.
func (c *Cache) All() []Record {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Record, 0, len(c.records))
	for _, r := range c.records {
		out = append(out, *r)
	}
	return out
}
