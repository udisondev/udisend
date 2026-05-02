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
// (closest to self last).
func (rt *RoutingTable) All() []Contact {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	var out []Contact
	for _, b := range rt.buckets {
		if b == nil {
			continue
		}
		out = append(out, b.snapshot()...)
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

// distanceCompare returns -1/0/+1 by XOR distance from target — the comparator
// shape slices.SortFunc expects, equivalent to dht.Less for sorting.
func distanceCompare(a, b, target NodeID) int {
	da, db := Distance(a, target), Distance(b, target)
	for i := range da {
		if da[i] != db[i] {
			if da[i] < db[i] {
				return -1
			}

			return 1
		}
	}

	return 0
}
