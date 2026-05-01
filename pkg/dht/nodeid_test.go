package dht_test

import (
	"testing"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
)

func id(hex string) dht.NodeID {
	if len(hex) != 32 {
		panic("test id must be 32 hex chars")
	}
	h, err := identity.ParseHash(hex)
	if err != nil {
		panic(err)
	}
	return h
}

func TestDistance_Symmetric(t *testing.T) {
	t.Parallel()
	a := id("00000000000000000000000000000001")
	b := id("00000000000000000000000000000002")
	if dht.Distance(a, b) != dht.Distance(b, a) {
		t.Fatal("distance should be symmetric")
	}
}

func TestDistance_SelfIsZero(t *testing.T) {
	t.Parallel()
	a := id("deadbeefdeadbeefdeadbeefdeadbeef")
	if dht.Distance(a, a) != (dht.NodeID{}) {
		t.Fatal("distance to self must be zero")
	}
}

func TestPrefixLen_Cases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"00000000000000000000000000000000", "00000000000000000000000000000000", 128},
		{"00000000000000000000000000000000", "80000000000000000000000000000000", 0},
		{"00000000000000000000000000000000", "40000000000000000000000000000000", 1},
		{"00000000000000000000000000000000", "00000000000000000000000000000001", 127},
		{"ff000000000000000000000000000000", "ff800000000000000000000000000000", 8},
	}
	for _, c := range cases {
		got := dht.PrefixLen(id(c.a), id(c.b))
		if got != c.want {
			t.Errorf("PrefixLen(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestLess_TotalOrder(t *testing.T) {
	t.Parallel()
	a := id("00000000000000000000000000000001")
	b := id("00000000000000000000000000000002")
	c := id("00000000000000000000000000000003")
	if !dht.Less(a, b) {
		t.Fatal("a < b expected")
	}
	if dht.Less(c, a) {
		t.Fatal("c < a should be false")
	}
}

func TestBucketIndex(t *testing.T) {
	t.Parallel()
	self := id("00000000000000000000000000000000")
	if got := dht.BucketIndex(self, self); got != -1 {
		t.Fatalf("BucketIndex(self, self) = %d, want -1", got)
	}
	cases := []struct {
		other string
		want  int
	}{
		{"80000000000000000000000000000000", 0},
		{"40000000000000000000000000000000", 1},
		{"00000000000000000000000000000001", 127},
	}
	for _, c := range cases {
		if got := dht.BucketIndex(self, id(c.other)); got != c.want {
			t.Errorf("BucketIndex(0,%s) = %d, want %d", c.other, got, c.want)
		}
	}
}
