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

// DefaultMemoryStoreEntries caps the number of distinct keys a
// MemoryStore retains. Sized so an attacker spraying STORE RPCs at the
// 4 KiB MaxStoreValue limit cannot force more than ~64 MiB of resident
// state — large enough for a real-world presence-store population, small
// enough to bound DoS amplification on any volunteer node.
const DefaultMemoryStoreEntries = 16384

// MemoryStore is an in-memory Store with TTL eviction. It performs NO
// validation on Put — every write is accepted. For production use the
// caller MUST wrap this (or any other plain Store) in a validator
// such as `presence.NewRateLimitedStore` which rejects unsigned
// records and rate-limits per source. The Phase 9 audit flagged that
// a bare MemoryStore on the network was indistinguishable from a
// public bulletin-board.
//
// MaxEntries hard-caps the number of stored keys. When the cap is hit
// and a new key arrives, the entry with the soonest expiry is evicted
// (an attacker spraying low-TTL records gets their own records dropped
// first; long-lived legitimate records survive). Zero means
// DefaultMemoryStoreEntries; a negative value disables capping (testing
// only).
type MemoryStore struct {
	mu         sync.RWMutex
	entries    map[NodeID]storeEntry
	now        func() time.Time
	MaxEntries int
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
// same key replace the prior value (no eviction triggered). When
// inserting a fresh key past MaxEntries, the entry with the soonest
// expiry is evicted to make room.
func (s *MemoryStore) Put(key NodeID, value []byte, ttl time.Duration) {
	cp := make([]byte, len(value))
	copy(cp, value)
	exp := s.now().Add(ttl)
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.entries[key]; !exists {
		s.evictIfFullLocked()
	}
	s.entries[key] = storeEntry{value: cp, expires: exp}
}

// evictIfFullLocked drops the entry with the soonest expiry when the
// map size has reached the configured cap. Caller MUST hold s.mu.
// Linear-scan is O(n); MaxEntries is bounded (16K default) so this
// remains under microseconds even at the cap. A more elaborate priority
// queue would be premature.
func (s *MemoryStore) evictIfFullLocked() {
	limit := s.MaxEntries
	if limit == 0 {
		limit = DefaultMemoryStoreEntries
	}
	if limit < 0 || len(s.entries) < limit {
		return
	}

	var (
		victim  NodeID
		earliest time.Time
		set      bool
	)
	for k, e := range s.entries {
		if !set || e.expires.Before(earliest) {
			victim = k
			earliest = e.expires
			set = true
		}
	}
	if set {
		delete(s.entries, victim)
	}
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
