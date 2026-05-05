package topotest_test

import (
	mathrand "math/rand/v2"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/topotest"
)

// §1 — form-of-graph tests. For each topology shape, every peer
// should be able to LookupNode every other peer's NodeID via
// iterative FIND_NODE. The seed edges define initial connectivity;
// Kademlia's recursive lookup is what ultimately connects everyone.

func TestForm_Chain(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{})
	c.Spawn(t, 6)
	topotest.BuildChain(t, c)

	c.LookupAcross(t)
}

func TestForm_Ring(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{})
	c.Spawn(t, 6)
	topotest.BuildRing(t, c)

	c.LookupAcross(t)
}

func TestForm_Star(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{})
	c.Spawn(t, 8)
	topotest.BuildStar(t, c)

	c.LookupAcross(t)
}

func TestForm_MultiHub(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{LookupTimeout: 6 * time.Second})
	c.Spawn(t, 9)
	topotest.BuildMultiHub(t, c, 3) // 3 hubs of 3 peers each

	c.LookupAcross(t)
}

func TestForm_FullMesh(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{})
	c.Spawn(t, 6)
	topotest.BuildFullMesh(t, c)

	// In a full mesh after Connect, every peer already has every
	// other in its routing table once back-probes settle, but we
	// still verify via lookup for parity with the other shapes.
	c.LookupAcross(t)
}

func TestForm_Random(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{LookupTimeout: 6 * time.Second})
	c.Spawn(t, 10)
	r := mathrand.New(mathrand.NewPCG(0xCAFE, 0xBABE))
	// 2N edges → expected average degree 4, almost-surely connected.
	topotest.BuildRandom(t, c, r, 20)

	c.LookupAcross(t)
}

func TestForm_BridgedClusters(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{LookupTimeout: 6 * time.Second})
	c.Spawn(t, 8)
	topotest.BuildBridgedClusters(t, c)

	c.LookupAcross(t)
}

func TestForm_KademliaNatural(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{LookupTimeout: 6 * time.Second})
	c.Spawn(t, 8)
	topotest.BuildKademliaNatural(t, c)

	c.LookupAcross(t)
}
