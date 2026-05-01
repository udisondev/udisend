// Package dht implements the Kademlia distributed hash table that udisend
// uses for peer discovery and presence storage. The ID space is 128 bits
// (each node is keyed by its 16-byte destination hash from pkg/identity),
// making node IDs and storage keys interchangeable.
//
// This implementation prioritises correctness and clarity over the full
// S/Kademlia hardening described in the design doc — sibling lists,
// adaptive PoW on node IDs, and disjoint-paths lookup are deferred (see
// ASSUMPTIONS.md). What's here: routing table with k-buckets, iterative
// FIND_NODE / FIND_VALUE / STORE / PING, and signed presence storage.
package dht

import (
	"github.com/udisondev/udisend/pkg/identity"
)

// NodeID is the routing identifier — an alias for identity.Hash so node
// IDs and content keys live in the same 128-bit space.
type NodeID = identity.Hash

// IDBits is the size of the ID space in bits.
const IDBits = 8 * identity.HashSize // 128

// Distance returns the XOR distance between two IDs. The result is a
// NodeID so callers can sort by it directly.
func Distance(a, b NodeID) NodeID {
	var d NodeID
	for i := range a {
		d[i] = a[i] ^ b[i]
	}
	return d
}

// PrefixLen returns the length (in bits) of the longest common prefix
// between a and b. Two equal IDs have prefix length IDBits.
func PrefixLen(a, b NodeID) int {
	for i := range a {
		x := a[i] ^ b[i]
		if x == 0 {
			continue
		}
		// Count leading zeros in the byte.
		for j := 7; j >= 0; j-- {
			if x&(1<<uint(j)) != 0 {
				return i*8 + (7 - j)
			}
		}
	}
	return IDBits
}

// Less reports whether distance da is closer to target than db.
func Less(da, db NodeID) bool {
	for i := range da {
		if da[i] != db[i] {
			return da[i] < db[i]
		}
	}
	return false
}

// BucketIndex is the routing-table bucket that a contact with id falls
// into, relative to self. Returns -1 for self.
func BucketIndex(self, id NodeID) int {
	if self == id {
		return -1
	}
	return PrefixLen(self, id)
}
