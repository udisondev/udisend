package dht

import (
	"net"
	"time"
)

// DefaultK is the standard Kademlia bucket size.
const DefaultK = 20

// MaxContactsPerSubnet bounds how many contacts can live in a single
// k-bucket from the same /24 IPv4 (or /64 IPv6) prefix. This defends
// against a trivial Sybil amplifier: a single attacker-controlled host
// can otherwise claim arbitrarily many NodeIDs all keyed on its own IP,
// biasing the local routing table.
const MaxContactsPerSubnet = 2

// bucket holds up to k contacts ordered most-recently-seen-last.
//
// Eviction policy (overnight scope): when a full bucket receives a new
// contact we keep the existing K — the newcomer is dropped. The full
// Kademlia heuristic (ping LRU, evict iff dead) is deferred; this is safe
// for small networks but reduces churn handling. See ASSUMPTIONS.md.
type bucket struct {
	cap      int
	contacts []Contact
}

func newBucket(k int) *bucket {
	return &bucket{cap: k, contacts: make([]Contact, 0, k)}
}

func (b *bucket) add(c Contact, now time.Time) (added, refreshed bool) {
	c.LastSeen = now
	for i := range b.contacts {
		if b.contacts[i].ID == c.ID {
			b.contacts[i] = c
			// move to end (most-recently-seen)
			b.contacts = append(append(b.contacts[:i], b.contacts[i+1:]...), c)
			return false, true
		}
	}
	if subnetOver(b.contacts, c.Addr, MaxContactsPerSubnet) {
		return false, false
	}
	if len(b.contacts) < b.cap {
		b.contacts = append(b.contacts, c)
		return true, false
	}
	return false, false
}

// subnetOver reports whether `existing` already holds limit-or-more
// contacts whose Addr shares the same /24 (IPv4) or /64 (IPv6) prefix
// as `addr`. A nil addr (test/in-memory transport) is exempt.
func subnetOver(existing []Contact, addr net.Addr, limit int) bool {
	if addr == nil || limit <= 0 {
		return false
	}
	prefix := addrSubnet(addr)
	if prefix == "" {
		return false
	}
	count := 0
	for _, c := range existing {
		if addrSubnet(c.Addr) == prefix {
			count++
			if count >= limit {
				return true
			}
		}
	}

	return false
}

// addrSubnet returns a string keying the /24 IPv4 prefix or /64 IPv6
// prefix of addr. Empty for non-IP addresses (e.g. in-memory pipe
// transports used by tests) AND for loopback addresses (localhost
// demos and test harnesses spin many peers on 127.0.0.x — refusing
// them would force every test to disable the cap manually).
func addrSubnet(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if ip.IsLoopback() {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return string(v4[:3])
	}
	v6 := ip.To16()
	if v6 == nil {
		return ""
	}

	return string(v6[:8])
}

func (b *bucket) remove(id NodeID) bool {
	for i := range b.contacts {
		if b.contacts[i].ID == id {
			b.contacts = append(b.contacts[:i], b.contacts[i+1:]...)
			return true
		}
	}
	return false
}

