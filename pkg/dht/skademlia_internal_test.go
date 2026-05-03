package dht

import (
	"sync"
	"testing"
	"time"
)

func mkContact(idHex, addr string) Contact {
	return Contact{ID: parseHexID(idHex), Addr: testAddr(addr), LastSeen: time.Unix(0, 0)}
}

type testAddr string

func (t testAddr) Network() string { return "test" }
func (t testAddr) String() string  { return string(t) }

func parseHexID(s string) NodeID {
	var out NodeID
	for i := 0; i < len(s) && i/2 < len(out); i += 2 {
		var b byte
		for j := 0; j < 2 && i+j < len(s); j++ {
			c := s[i+j]
			var v byte
			switch {
			case c >= '0' && c <= '9':
				v = c - '0'
			case c >= 'a' && c <= 'f':
				v = c - 'a' + 10
			case c >= 'A' && c <= 'F':
				v = c - 'A' + 10
			}
			b = b<<4 | v
		}
		out[i/2] = b
	}
	return out
}

// TestClaimBatch_RespectsDisjointVisitedSet verifies that once a contact
// has been claimed by one path's claimBatch, a second path cannot claim
// the same contact. This is the S-Kademlia §4.2 disjointness property
// reduced to its smallest unit-testable form.
func TestClaimBatch_RespectsDisjointVisitedSet(t *testing.T) {
	t.Parallel()

	c1 := mkContact("01000000000000000000000000000000", "1")
	c2 := mkContact("02000000000000000000000000000000", "2")
	c3 := mkContact("03000000000000000000000000000000", "3")

	pathA := &pathState{
		shortlist: []Contact{c1, c2, c3},
		queried:   make(map[NodeID]bool),
	}
	pathB := &pathState{
		shortlist: []Contact{c1, c2, c3}, // same seeds — pathological case
		queried:   make(map[NodeID]bool),
	}

	var visitedMu sync.Mutex
	visited := make(map[NodeID]bool)

	n := &Node{cfg: Config{Alpha: 3}}

	batchA := n.claimBatch(pathA, &visitedMu, visited)
	if len(batchA) != 3 {
		t.Fatalf("pathA claimed %d, want 3", len(batchA))
	}

	batchB := n.claimBatch(pathB, &visitedMu, visited)
	if len(batchB) != 0 {
		t.Fatalf("pathB claimed %d contacts that pathA already took (disjointness violated)",
			len(batchB))
	}
}

// TestClaimBatch_BoundedByAlpha checks that no single call returns more
// than Config.Alpha contacts.
func TestClaimBatch_BoundedByAlpha(t *testing.T) {
	t.Parallel()

	cs := []Contact{
		mkContact("01000000000000000000000000000000", "1"),
		mkContact("02000000000000000000000000000000", "2"),
		mkContact("03000000000000000000000000000000", "3"),
		mkContact("04000000000000000000000000000000", "4"),
		mkContact("05000000000000000000000000000000", "5"),
	}

	p := &pathState{shortlist: cs, queried: make(map[NodeID]bool)}
	var visitedMu sync.Mutex
	visited := make(map[NodeID]bool)

	n := &Node{cfg: Config{Alpha: 2}}
	got := n.claimBatch(p, &visitedMu, visited)
	if len(got) != 2 {
		t.Fatalf("claimBatch returned %d, want 2 (alpha)", len(got))
	}
}

// TestMergePathShortlists_DedupsAndSorts asserts the per-path shortlist
// merger produces a unique list sorted by XOR distance to target,
// truncated to k.
func TestMergePathShortlists_DedupsAndSorts(t *testing.T) {
	t.Parallel()

	target := parseHexID("00000000000000000000000000000000")

	c1 := mkContact("00000000000000000000000000000010", "near")
	c2 := mkContact("00000000000000000000000000000040", "mid")
	c3 := mkContact("80000000000000000000000000000000", "far")

	a := &pathState{shortlist: []Contact{c2, c3}}
	b := &pathState{shortlist: []Contact{c1, c2}} // c2 dup with a

	got := mergePathShortlists([]*pathState{a, b}, target, 3)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3 (deduped)", len(got))
	}

	if got[0].Addr.String() != "near" {
		t.Fatalf("got[0] = %v, want near", got[0].Addr)
	}

	if got[2].Addr.String() != "far" {
		t.Fatalf("got[2] = %v, want far", got[2].Addr)
	}
}

// TestMergePathShortlists_TruncatesToK trims excess.
func TestMergePathShortlists_TruncatesToK(t *testing.T) {
	t.Parallel()

	target := parseHexID("00000000000000000000000000000000")
	a := &pathState{shortlist: []Contact{
		mkContact("00000000000000000000000000000010", "1"),
		mkContact("00000000000000000000000000000020", "2"),
		mkContact("00000000000000000000000000000030", "3"),
		mkContact("00000000000000000000000000000040", "4"),
	}}

	got := mergePathShortlists([]*pathState{a}, target, 2)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

// TestMergeContacts_DedupsAcrossSlices is a unit test for the helper
// PutValue uses to combine its lookup result with the sibling list.
func TestMergeContacts_DedupsAcrossSlices(t *testing.T) {
	t.Parallel()

	c1 := mkContact("01000000000000000000000000000000", "1")
	c2 := mkContact("02000000000000000000000000000000", "2")
	c3 := mkContact("03000000000000000000000000000000", "3")

	got := mergeContacts([]Contact{c1, c2}, []Contact{c2, c3})
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].ID != c1.ID || got[1].ID != c2.ID || got[2].ID != c3.ID {
		t.Fatalf("order/dedup wrong: %v", got)
	}
}
