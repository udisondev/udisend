package topotest

import (
	mathrand "math/rand/v2"
	"testing"
)

// Edge is a directed seed edge: A pings B. After Connect, A learns B
// synchronously and B learns A asynchronously through the
// verification probe. The graph that emerges is undirected from the
// routing-table point of view, but the seed direction matters for
// tests that need to know who initiated.
type Edge struct{ A, B int }

// BuildChain wires peers as A0→A1→A2→…→A(n-1). Each peer only
// initially knows its immediate successor.
func BuildChain(t *testing.T, c *Cluster) {
	t.Helper()

	n := c.Len()
	for i := 0; i < n-1; i++ {
		c.Connect(t, i, i+1)
	}
}

// BuildRing wires peers as a chain plus the closing edge A(n-1)→A0.
func BuildRing(t *testing.T, c *Cluster) {
	t.Helper()

	BuildChain(t, c)
	n := c.Len()
	if n > 1 {
		c.Connect(t, n-1, 0)
	}
}

// BuildStar wires every peer to peer 0 (the hub).
func BuildStar(t *testing.T, c *Cluster) {
	t.Helper()

	n := c.Len()
	for i := 1; i < n; i++ {
		c.Connect(t, i, 0)
	}
}

// BuildMultiHub partitions peers into groups of size groupSize. Peer
// 0 of each group is the hub: it connects to every spoke in its
// group, and the hubs additionally form a full mesh between
// themselves. Tests use this to verify failover when one hub dies.
//
// Peers are taken in spawn order: [hub0, spoke, spoke, …, hub1, spoke,
// spoke, …]. groupSize must be ≥ 2.
func BuildMultiHub(t *testing.T, c *Cluster, groupSize int) {
	t.Helper()

	if groupSize < 2 {
		t.Fatalf("topotest: BuildMultiHub groupSize=%d must be ≥ 2", groupSize)
	}
	n := c.Len()

	hubs := []int{}
	for i := 0; i < n; i += groupSize {
		hubs = append(hubs, i)
		end := min(i+groupSize, n)
		for j := i + 1; j < end; j++ {
			c.Connect(t, j, i)
		}
	}
	for i, h := range hubs {
		for _, h2 := range hubs[i+1:] {
			c.Connect(t, h, h2)
		}
	}
}

// BuildFullMesh wires every pair (i, j) with i < j. O(N²) edges, only
// suitable for small N.
func BuildFullMesh(t *testing.T, c *Cluster) {
	t.Helper()

	n := c.Len()
	for i := range n {
		for j := i + 1; j < n; j++ {
			c.Connect(t, i, j)
		}
	}
}

// BuildRandom wires a random graph with `edges` undirected seed
// edges. The graph is NOT guaranteed connected — tests should pick
// `edges` ≥ N to make connectivity likely. A fixed math/rand/v2
// source is used so failures are reproducible from the seed.
func BuildRandom(t *testing.T, c *Cluster, r *mathrand.Rand, edges int) {
	t.Helper()

	n := c.Len()
	if n < 2 {
		return
	}
	picked := map[[2]int]struct{}{}
	for len(picked) < edges {
		a := r.IntN(n)
		b := r.IntN(n)
		if a == b {
			continue
		}
		if a > b {
			a, b = b, a
		}
		key := [2]int{a, b}
		if _, dup := picked[key]; dup {
			continue
		}
		picked[key] = struct{}{}
		c.Connect(t, a, b)
	}
}

// BuildBridgedClusters splits peers into two equal-ish clusters,
// forms a chain inside each (so each cluster is connected), and
// connects them through a single bridge edge between the last peer
// of the first cluster and the first peer of the second.
//
// Useful for partition tests: detach the bridge and the two clusters
// can no longer reach each other.
func BuildBridgedClusters(t *testing.T, c *Cluster) (left, right []int, bridge Edge) {
	t.Helper()

	n := c.Len()
	if n < 4 {
		t.Fatalf("topotest: BuildBridgedClusters needs ≥ 4 peers, got %d", n)
	}
	mid := n / 2
	for i := 0; i < mid-1; i++ {
		c.Connect(t, i, i+1)
	}
	for i := mid; i < n-1; i++ {
		c.Connect(t, i, i+1)
	}
	bridge = Edge{A: mid - 1, B: mid}
	c.Connect(t, bridge.A, bridge.B)

	left = make([]int, mid)
	for i := range left {
		left[i] = i
	}
	right = make([]int, n-mid)
	for i := range right {
		right[i] = mid + i
	}

	return left, right, bridge
}

// BuildKademliaNatural connects every peer to one common bootstrap
// peer (peer 0) via Bootstrap (not just Connect), letting Kademlia's
// own iterative lookup populate the routing tables organically. This
// is the closest in-process analog of "real" deployment where every
// new client knows only the community bootstrap address. After this
// the cluster has converged on its own.
func BuildKademliaNatural(t *testing.T, c *Cluster) {
	t.Helper()

	n := c.Len()
	for i := 1; i < n; i++ {
		c.Bootstrap(t, i, 0)
	}
}
