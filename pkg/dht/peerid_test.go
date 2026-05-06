package dht_test

import (
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
)

// fakePeerID is a minimal identity.PeerID implementation that does not
// depend on identity.Hash. The Phase 11.3 boundary contract guarantees
// that pkg/dht accepts any PeerID at its public API; this test proves
// the contract by using a foreign type that satisfies the interface.
type fakePeerID struct {
	id  [identity.HashSize]byte
	tag string
}

func (f fakePeerID) Bytes() [identity.HashSize]byte { return f.id }

func (f fakePeerID) String() string { return "fake:" + f.tag }

// Compile-time assertion: fakePeerID satisfies identity.PeerID.
var _ identity.PeerID = fakePeerID{}

func TestRoutingTable_AcceptsForeignPeerID(t *testing.T) {
	t.Parallel()

	self := fakePeerID{id: [identity.HashSize]byte{0xAA}, tag: "self"}
	rt := dht.NewRoutingTable(self, 8)

	if rt.Self() != self.Bytes() {
		t.Fatalf("Self() = %x, want %x", rt.Self(), self.Bytes())
	}

	contactHash := identity.Hash{0xBB, 0xCC}
	rt.Add(dht.Contact{ID: contactHash, LastSeen: time.Now()})

	target := fakePeerID{id: [identity.HashSize]byte{0xBB}, tag: "target"}
	closest := rt.Closest(target, 8)
	if len(closest) != 1 {
		t.Fatalf("Closest returned %d contacts, want 1", len(closest))
	}
	if closest[0].ID != contactHash {
		t.Fatalf("Closest returned %x, want %x", closest[0].ID, contactHash)
	}

	got, ok := rt.Contact(fakePeerID{id: contactHash})
	if !ok {
		t.Fatalf("Contact(fakePeerID) reported missing entry")
	}
	if got.ID != contactHash {
		t.Fatalf("Contact ID mismatch: %x vs %x", got.ID, contactHash)
	}

	if !rt.Remove(fakePeerID{id: contactHash}) {
		t.Fatalf("Remove(fakePeerID) returned false for known contact")
	}
}

func TestMemoryStore_AcceptsForeignPeerID(t *testing.T) {
	t.Parallel()

	store := dht.NewMemoryStore(nil)
	key := fakePeerID{id: [identity.HashSize]byte{0x11, 0x22, 0x33}, tag: "key"}
	value := []byte("hello phase 11")

	store.Put(key, value, time.Hour)

	got, ok := store.Get(key)
	if !ok {
		t.Fatalf("Get returned no value for key just stored")
	}
	if string(got) != string(value) {
		t.Fatalf("Get value = %q, want %q", got, value)
	}

	// And via identity.Hash with the same bytes — must hit the same slot.
	hashKey := identity.Hash(key.Bytes())
	got2, ok := store.Get(hashKey)
	if !ok || string(got2) != string(value) {
		t.Fatalf("PeerID and Hash with same bytes resolved to different slots")
	}
}

