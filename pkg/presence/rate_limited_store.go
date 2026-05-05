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

// DefaultMaxTrackedIPs caps the cardinality of the per-IP bookkeeping
// maps. Even with MaxPerIP=16, an attacker spraying 1M source IPs (XFF
// spoofing through misconfigured proxy, IPv6 /64 churn, real botnet)
// can grow `keyByIP`/`ipByKey` without bound between Sweep calls — the
// limit only applies per-IP. 100K distinct IPs × ~64 B/entry ≈ 6 MB,
// large enough for legitimate fanout, small enough to bound DoS.
const DefaultMaxTrackedIPs = 100_000

// RateLimitedStore wraps a dht.Store and enforces a per-source-IP cap on
// stored presence records. It implements dht.SourcedStore so the DHT
// node hands the real packet origin (`net.Addr`) — keying the cap on
// the claimed `Record.Address` would let an attacker pin the cap on a
// victim's IP and lock them out.
//
// Records that fail signature verification are dropped before any
// counter is touched (no slot is consumed by garbage). Non-presence
// blobs (anything that fails to unmarshal as Record) pass through to
// the inner store unchanged: the rate limit applies to presence only.
type RateLimitedStore struct {
	inner dht.Store

	// MaxPerIP is the per-IP record cap. Zero means DefaultMaxRecordsPerIP.
	// Negative disables limiting (useful for tests).
	MaxPerIP int

	// MaxTrackedIPs hard-caps the cardinality of the bookkeeping maps.
	// When the cap is hit, fresh source IPs are refused (their records
	// pass through without quota) — losing rate-limiting for new IPs is
	// strictly safer than letting the map grow unbounded under spoof
	// floods. Zero means DefaultMaxTrackedIPs; negative disables.
	MaxTrackedIPs int

	// VerifyTTL bounds record IssuedAt freshness during signature
	// validation. Zero means "do not enforce expiry" — verify the
	// signature only.
	VerifyTTL time.Duration

	// Clock returns the wall-clock used for verification freshness.
	// Defaults to time.Now.
	Clock func() time.Time

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

// Put is the unsourced path — used by callers that do not know the
// network origin (local writes, tests). It applies no per-IP limit
// because there is no "per IP" to apply against.
func (s *RateLimitedStore) Put(key dht.NodeID, value []byte, ttl time.Duration) {
	s.inner.Put(key, value, ttl)
}

// PutFromSource is the rate-limited entry point used by the DHT node
// when a STORE RPC arrives from `source`. Presence records are decoded
// + signature-verified before any slot is allocated. Mismatches between
// the record's claimed Address and `source` are tolerated (peers behind
// NAT routinely have them) — the cap keys on `source` only.
func (s *RateLimitedStore) PutFromSource(key dht.NodeID, value []byte, ttl time.Duration, source net.Addr) {
	ip := addrIP(source)
	if ip == "" {
		s.inner.Put(key, value, ttl)
		return
	}

	var rec Record
	if err := rec.UnmarshalBinary(value); err != nil {
		// Not a presence record — pass through; the rate limit applies
		// only to the presence layer.
		s.inner.Put(key, value, ttl)
		return
	}

	now := s.now()
	if err := rec.Verify(now, s.VerifyTTL); err != nil {
		// Forged or stale records do not consume a slot.
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

	tracked := s.MaxTrackedIPs
	if tracked == 0 {
		tracked = DefaultMaxTrackedIPs
	}

	s.mu.Lock()
	prev, isUpdate := s.ipByKey[key]
	keys, ipKnown := s.keyByIP[ip]
	if !isUpdate && len(keys) >= limit {
		s.mu.Unlock()
		return
	}
	// Cardinality cap: refuse to allocate a fresh IP slot once the
	// tracking map is full. The record itself still goes to the inner
	// store (so legitimate one-off publishers under heavy churn aren't
	// silently dropped), it just bypasses the rate-limit. Sweep drains
	// this back down once stale records expire.
	if !ipKnown && !isUpdate && tracked > 0 && len(s.keyByIP) >= tracked {
		s.mu.Unlock()
		s.inner.Put(key, value, ttl)
		return
	}
	if isUpdate && prev != ip {
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

// TrackedIPCount reports how many distinct source IPs the limiter is
// currently bookkeeping. Test-facing — exposed for the cardinality-cap
// regression suite.
func (s *RateLimitedStore) TrackedIPCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.keyByIP)
}

// Sweep delegates to the inner store and prunes per-IP bookkeeping for
// keys the inner store no longer holds — so an attacker churning
// short-TTL records does not permanently consume a victim IP's slot
// budget.
func (s *RateLimitedStore) Sweep(now time.Time) {
	s.inner.Sweep(now)

	s.mu.Lock()
	defer s.mu.Unlock()

	for key, ip := range s.ipByKey {
		if _, alive := s.inner.Get(key); alive {
			continue
		}
		delete(s.ipByKey, key)
		if owned := s.keyByIP[ip]; owned != nil {
			delete(owned, key)
			if len(owned) == 0 {
				delete(s.keyByIP, ip)
			}
		}
	}
}

func (s *RateLimitedStore) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}

	return time.Now().UTC()
}

// addrIP extracts the host portion of a net.Addr so the rate-limiter
// keys on subnet/host rather than ephemeral source ports.
//
// For *net.UDPAddr we use the raw IP bytes as a string — this is one
// stdlib-intrinsic byte→string allocation per call instead of the
// IP.String() formatter pass. The caller only uses the value as a map
// key, where two equal IP byte sequences yield equal strings.
func addrIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	if u, ok := addr.(*net.UDPAddr); ok && len(u.IP) > 0 {
		return string(u.IP)
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}

	return host
}
