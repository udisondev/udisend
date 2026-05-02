package dht

import (
	"net"
	"sync"
	"time"
)

// Store is the local key/value store used by DHT nodes to keep replicated
// presence records (and any other data the network agrees to host).
type Store interface {
	Put(key NodeID, value []byte, ttl time.Duration)
	Get(key NodeID) ([]byte, bool)
	// Sweep removes entries whose TTL has elapsed relative to `now`.
	Sweep(now time.Time)
}

// SourcedStore is an optional Store extension. When the DHT node hands
// a STORE RPC to a Store that implements this interface, the source
// address of the packet is passed too so per-source-IP defenses
// (Sybil rate-limit, abuse heuristics) can key on the real network
// origin rather than fields the publisher claims about themselves.
type SourcedStore interface {
	Store
	PutFromSource(key NodeID, value []byte, ttl time.Duration, source net.Addr)
}

// MemoryStore is the default in-memory Store with TTL eviction.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[NodeID]storeEntry
	now     func() time.Time
}

type storeEntry struct {
	value   []byte
	expires time.Time
}

// NewMemoryStore returns an empty store. The optional now function is used
// for time; pass nil to use time.Now.
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{entries: make(map[NodeID]storeEntry), now: now}
}

// Put stores value under key with the given TTL. Subsequent Puts on the
// same key replace the prior value.
func (s *MemoryStore) Put(key NodeID, value []byte, ttl time.Duration) {
	cp := make([]byte, len(value))
	copy(cp, value)
	exp := s.now().Add(ttl)
	s.mu.Lock()
	s.entries[key] = storeEntry{value: cp, expires: exp}
	s.mu.Unlock()
}

// Get returns the stored value if present and not expired.
func (s *MemoryStore) Get(key NodeID) ([]byte, bool) {
	s.mu.RLock()
	e, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if !e.expires.IsZero() && !e.expires.After(s.now()) {
		return nil, false
	}
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, true
}

// Sweep removes expired entries.
func (s *MemoryStore) Sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.entries {
		if !e.expires.IsZero() && !e.expires.After(now) {
			delete(s.entries, k)
		}
	}
}

// Size reports how many keys are stored. Mostly useful in tests.
func (s *MemoryStore) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}
