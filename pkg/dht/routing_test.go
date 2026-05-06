package dht_test

import (
	"net"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
)

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

func contact(idHex string, addr string) dht.Contact {
	return dht.Contact{ID: id(idHex), Addr: fakeAddr(addr), LastSeen: time.Unix(0, 0)}
}

func TestRoutingTable_AddAndSize(t *testing.T) {
	t.Parallel()
	rt := dht.NewRoutingTable(id("00000000000000000000000000000000"), 4)
	rt.Add(contact("80000000000000000000000000000000", "a"))
	rt.Add(contact("40000000000000000000000000000000", "b"))
	rt.Add(contact("80000000000000000000000000000000", "a")) // dedup
	if rt.Size() != 2 {
		t.Fatalf("Size = %d, want 2", rt.Size())
	}
}

func TestRoutingTable_RejectsSelf(t *testing.T) {
	t.Parallel()
	self := id("00000000000000000000000000000000")
	rt := dht.NewRoutingTable(self, 4)
	rt.Add(contact("00000000000000000000000000000000", "self"))
	if rt.Size() != 0 {
		t.Fatalf("self should not be added")
	}
}

func TestRoutingTable_Closest_Sorted(t *testing.T) {
	t.Parallel()
	rt := dht.NewRoutingTable(id("00000000000000000000000000000000"), 8)
	rt.Add(contact("80000000000000000000000000000000", "far"))
	rt.Add(contact("00000000000000000000000000000010", "near"))
	rt.Add(contact("00010000000000000000000000000000", "mid"))

	target := id("00000000000000000000000000000001")
	out := rt.Closest(target, 3)
	if len(out) != 3 {
		t.Fatalf("Closest returned %d, want 3", len(out))
	}
	if out[0].Addr.(fakeAddr) != "near" {
		t.Fatalf("nearest contact wrong: %v", out[0].Addr)
	}
}

func TestRoutingTable_Closest_LimitsBucketSize(t *testing.T) {
	t.Parallel()
	rt := dht.NewRoutingTable(id("00000000000000000000000000000000"), 2)
	// Same bucket: both have prefix len 0 → bucket 0
	rt.Add(contact("80000000000000000000000000000000", "1"))
	rt.Add(contact("c0000000000000000000000000000000", "2"))
	rt.Add(contact("e0000000000000000000000000000000", "3"))
	if rt.Size() != 2 {
		t.Fatalf("bucket cap of 2 not honoured (size=%d)", rt.Size())
	}
}

func TestRoutingTable_Remove(t *testing.T) {
	t.Parallel()
	rt := dht.NewRoutingTable(id("00000000000000000000000000000000"), 4)
	rt.Add(contact("80000000000000000000000000000000", "a"))
	if !rt.Remove(id("80000000000000000000000000000000")) {
		t.Fatal("Remove returned false for known id")
	}
	if rt.Size() != 0 {
		t.Fatalf("size after remove = %d, want 0", rt.Size())
	}
	if rt.Remove(id("80000000000000000000000000000000")) {
		t.Fatal("Remove returned true for unknown id")
	}
}

// fakeAddr also satisfies net.Addr — keep this anchor so we notice if the
// interface drifts.
var _ net.Addr = fakeAddr("")
