package dht

import "time"

// DefaultK is the standard Kademlia bucket size.
const DefaultK = 20

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
	if len(b.contacts) < b.cap {
		b.contacts = append(b.contacts, c)
		return true, false
	}
	return false, false
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

func (b *bucket) snapshot() []Contact {
	out := make([]Contact, len(b.contacts))
	copy(out, b.contacts)
	return out
}
