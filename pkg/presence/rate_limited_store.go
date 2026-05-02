package presence

import (
	"net"
	"sync"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
)

// DefaultMaxRecordsPerIP caps how many presence records a single source
// IP may have stored simultaneously on a relay/network node. design.md
// §8 ("Sybil на presence — лимит на записи с одного IP"). Tunable via
// RateLimitedStore.MaxPerIP.
const DefaultMaxRecordsPerIP = 16

// RateLimitedStore wraps a dht.Store and enforces a per-source-IP cap on
// stored presence records. Non-presence values pass through unchanged
// (the underlying dht.Store sees them, the limit does not apply). Updates
// to an existing key by the same IP do not count against the limit.
type RateLimitedStore struct {
	inner dht.Store

	// MaxPerIP is the per-IP record cap. Zero means DefaultMaxRecordsPerIP.
	// Negative disables limiting (useful for tests).
	MaxPerIP int

	mu      sync.Mutex
	keyByIP map[string]map[dht.NodeID]struct{} // ip → set of keys it owns
	ipByKey map[dht.NodeID]string              // key → ip (for cleanup)
}

// NewRateLimitedStore wraps an existing dht.Store with per-IP rate limiting.
func NewRateLimitedStore(inner dht.Store) *RateLimitedStore {
	return &RateLimitedStore{
		inner:   inner,
		keyByIP: make(map[string]map[dht.NodeID]struct{}),
		ipByKey: make(map[dht.NodeID]string),
	}
}

// Put stores value under key. If the value parses as a presence Record
// and the source IP already owns MaxPerIP distinct keys (and key is new),
// the call is silently dropped — Sybil hosts can't flood a victim's
// neighbourhood with hundreds of fake records by spinning identities.
func (s *RateLimitedStore) Put(key dht.NodeID, value []byte, ttl time.Duration) {
	var rec Record
	if err := rec.UnmarshalBinary(value); err != nil {
		// Not a presence record — pass through without rate limiting.
		s.inner.Put(key, value, ttl)
		return
	}
	ip := extractIP(rec.Address)
	if ip == "" {
		s.inner.Put(key, value, ttl)
		return
	}
	limit := s.MaxPerIP
	if limit == 0 {
		limit = DefaultMaxRecordsPerIP
	}
	if limit < 0 {
		s.inner.Put(key, value, ttl)
		return
	}

	s.mu.Lock()
	prev, isUpdate := s.ipByKey[key]
	keys := s.keyByIP[ip]
	if !isUpdate && len(keys) >= limit {
		s.mu.Unlock()
		return
	}
	if isUpdate && prev != ip {
		// IP migrated for this key: tear down the old IP's slot.
		if old := s.keyByIP[prev]; old != nil {
			delete(old, key)
			if len(old) == 0 {
				delete(s.keyByIP, prev)
			}
		}
	}
	if keys == nil {
		keys = make(map[dht.NodeID]struct{})
		s.keyByIP[ip] = keys
	}
	keys[key] = struct{}{}
	s.ipByKey[key] = ip
	s.mu.Unlock()
	s.inner.Put(key, value, ttl)
}

// Get is a pass-through.
func (s *RateLimitedStore) Get(key dht.NodeID) ([]byte, bool) {
	return s.inner.Get(key)
}

// Sweep is a pass-through. Per-IP slot counters are NOT pruned for
// expired entries because we don't know the inner store's eviction; on
// restart the maps reset. This is acceptable for MVP — the cap is a
// soft bound, not a security boundary.
func (s *RateLimitedStore) Sweep(now time.Time) {
	s.inner.Sweep(now)
}

// extractIP pulls the host portion of an "ip:port" address. Returns "" if
// the address is malformed.
func extractIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr // best-effort: treat the whole string as the host
	}
	return host
}
