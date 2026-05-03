package dht

import (
	"slices"
	"sync"
	"time"
)

// RoutingTable is a Kademlia routing table over 128-bit IDs.
type RoutingTable struct {
	self NodeID
	k    int

	mu      sync.RWMutex
	buckets [IDBits]*bucket
}

// NewRoutingTable constructs a routing table for `self` with bucket size k.
func NewRoutingTable(self NodeID, k int) *RoutingTable {
	if k <= 0 {
		k = DefaultK
	}
	return &RoutingTable{self: self, k: k}
}

// Self returns the local node's ID.
func (rt *RoutingTable) Self() NodeID { return rt.self }

// K returns the configured bucket size.
func (rt *RoutingTable) K() int { return rt.k }

// Add inserts or refreshes a contact.
func (rt *RoutingTable) Add(c Contact) {
	if c.ID == rt.self {
		return
	}
	idx := BucketIndex(rt.self, c.ID)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	b := rt.buckets[idx]
	if b == nil {
		b = newBucket(rt.k)
		rt.buckets[idx] = b
	}
	now := c.LastSeen
	if now.IsZero() {
		now = time.Now()
	}
	b.add(c, now)
}

// Remove drops the contact with the given ID, if any.
func (rt *RoutingTable) Remove(id NodeID) bool {
	if id == rt.self {
		return false
	}
	idx := BucketIndex(rt.self, id)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	b := rt.buckets[idx]
	if b == nil {
		return false
	}
	return b.remove(id)
}

// Size returns the total number of contacts across all buckets.
func (rt *RoutingTable) Size() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	total := 0
	for _, b := range rt.buckets {
		if b != nil {
			total += len(b.contacts)
		}
	}
	return total
}

// All returns a snapshot of every known contact, sorted by bucket index
// (closest to self last). The returned slice is a fresh copy; mutating
// it never disturbs the table.
func (rt *RoutingTable) All() []Contact {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	total := 0
	for _, b := range rt.buckets {
		if b != nil {
			total += len(b.contacts)
		}
	}
	if total == 0 {
		return nil
	}

	out := make([]Contact, 0, total)
	for _, b := range rt.buckets {
		if b != nil {
			out = append(out, b.contacts...)
		}
	}

	return out
}

// Closest returns the n contacts with the smallest XOR distance to target.
// If fewer than n are known, returns whatever is available.
func (rt *RoutingTable) Closest(target NodeID, n int) []Contact {
	if n <= 0 {
		return nil
	}
	all := rt.All()
	slices.SortFunc(all, func(a, b Contact) int {
		return distanceCompare(a.ID, b.ID, target)
	})
	if len(all) > n {
		all = all[:n]
	}

	return all
}

// Siblings returns the s contacts with the smallest XOR distance to the
// local node — the sibling list from S-Kademlia §4.4. Used by PutValue
// to replicate values onto our own neighbourhood, raising the bar for a
// Sybil cluster trying to suppress a record by capturing only the K
// closest peers to the key. design.md §4.
func (rt *RoutingTable) Siblings(s int) []Contact {
	return rt.Closest(rt.self, s)
}

// distanceCompare returns -1/0/+1 by XOR distance from target — the comparator
// shape slices.SortFunc expects, equivalent to dht.Less for sorting. It
// computes XOR-distances inline byte-by-byte and stops at the first
// differing byte rather than materialising two full 16-byte arrays for
// each comparison; the sort runs O(n log n) so the savings compound.
func distanceCompare(a, b, target NodeID) int {
	for i := range a {
		da := a[i] ^ target[i]
		db := b[i] ^ target[i]
		if da != db {
			if da < db {
				return -1
			}

			return 1
		}
	}

	return 0
}
