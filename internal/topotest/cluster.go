// Package topotest is the in-process topology harness for udisend
// network tests. It wires N dht.Node instances onto a single
// transport.MemoryHub, lets the test seed arbitrary edges between
// them, and exposes helpers for asserting routing-table convergence.
//
// The harness only covers the DHT/signaling layer — WebRTC
// PeerConnections live in the browser (or pion in a future TUI), so
// "node availability" here means: can node A find node B's contact
// via the routing table and reach it through the iterative lookup.
package topotest

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// Defaults for the harness. Tunable per-test via Options.
const (
	DefaultRequestTimeout = 500 * time.Millisecond
	DefaultLookupTimeout  = 4 * time.Second
	DefaultConnectTimeout = 2 * time.Second
	DefaultWaitTimeout    = 5 * time.Second
)

// Options tweaks Cluster behaviour. Zero values fall back to the
// Default* constants above.
type Options struct {
	RequestTimeout time.Duration
	LookupTimeout  time.Duration
	ConnectTimeout time.Duration

	// Seed, when non-nil, supplies deterministic identity material so a
	// failing test can be re-run with the same NodeIDs. Zero seed →
	// crypto/rand.
	Seed io.Reader

	// K overrides Kademlia bucket size. Zero → dht.DefaultK.
	K int
	// Alpha overrides FIND_NODE concurrency. Zero → dht.DefaultAlpha.
	Alpha int
	// Disjoint overrides S/Kademlia disjoint paths. Zero → default.
	Disjoint int

	// WithSignaling, when true, attaches a signaling.Service to every
	// peer at Spawn time. The Service uses a shared static
	// AddressResolver (peer hash → its own MemoryAddr) and a Router
	// that resolves any cluster peer's hash → its MemoryAddr (used
	// for relay forwarding tests). Use the returned SigCluster
	// (Cluster.Sig) for the signaling-level helpers.
	WithSignaling bool
}

// Peer is a single node in the cluster.
type Peer struct {
	idx   int
	node  *dht.Node
	tr    *transport.MemoryTransport
	id    *identity.Identity
	stop  context.CancelFunc
	hub   *transport.MemoryHub
	hubID string

	// svc is non-nil when the cluster was created with
	// Options.WithSignaling.
	svc *signaling.Service
}

// Index returns this peer's position in the cluster.
func (p *Peer) Index() int { return p.idx }

// Node exposes the underlying DHT node.
func (p *Peer) Node() *dht.Node { return p.node }

// Transport exposes the in-memory transport bound to this peer.
func (p *Peer) Transport() *transport.MemoryTransport { return p.tr }

// ID returns the peer's NodeID.
func (p *Peer) ID() dht.NodeID { return p.node.ID() }

// HubID returns the MemoryHub registration key for this peer. Useful
// for hub.Swap() during partition / man-in-the-middle scenarios.
func (p *Peer) HubID() string { return p.hubID }

// Cluster is a collection of in-process DHT peers wired through one
// MemoryHub. Tests build topologies on top of it.
type Cluster struct {
	hub  *transport.MemoryHub
	opts Options

	mu    sync.RWMutex
	peers []*Peer
	wg    sync.WaitGroup

	// sig is non-nil when Options.WithSignaling is true. It owns the
	// per-peer signaling.Service instances and the shared resolver.
	sig *SigCluster
}

// New creates an empty cluster. Use Spawn to add peers.
func New(opts Options) *Cluster {
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	if opts.LookupTimeout == 0 {
		opts.LookupTimeout = DefaultLookupTimeout
	}
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = DefaultConnectTimeout
	}

	c := &Cluster{
		hub:  transport.NewMemoryHub(),
		opts: opts,
	}
	if opts.WithSignaling {
		c.sig = newSigCluster(c)
	}

	return c
}

// Sig returns the signaling layer attached to this cluster.
// Returns nil if Options.WithSignaling was false.
func (c *Cluster) Sig() *SigCluster { return c.sig }

// Hub exposes the underlying MemoryHub. Tests use this to swap
// transports for partition / blackhole scenarios.
func (c *Cluster) Hub() *transport.MemoryHub { return c.hub }

// Len returns the current peer count.
func (c *Cluster) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.peers)
}

// Peer returns the i-th peer. Panics on out-of-range — tests should
// keep their indices honest.
func (c *Cluster) Peer(i int) *Peer {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.peers[i]
}

// Peers returns a snapshot of all current peers in spawn order.
func (c *Cluster) Peers() []*Peer {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]*Peer, len(c.peers))
	copy(out, c.peers)

	return out
}

// Spawn adds n fresh peers to the cluster. Each peer gets a unique
// MemoryHub registration and starts its DHT receive loop. Returns the
// new peers in spawn order.
//
// The peers are NOT yet connected to anyone — call Connect or one of
// the topology builders to seed initial edges.
func (c *Cluster) Spawn(t *testing.T, n int) []*Peer {
	t.Helper()

	out := make([]*Peer, n)
	for i := range n {
		out[i] = c.spawnOne(t)
	}

	return out
}

func (c *Cluster) spawnOne(t *testing.T) *Peer {
	t.Helper()

	seed := c.opts.Seed
	if seed == nil {
		seed = rand.Reader
	}
	id, err := identity.Generate(seed)
	if err != nil {
		t.Fatalf("topotest: identity.Generate: %v", err)
	}

	tr := c.hub.NewMemoryTransport()
	hubID := tr.LocalAddr().(transport.MemoryAddr).ID()

	c.mu.Lock()
	idx := len(c.peers)
	c.mu.Unlock()

	p := &Peer{
		idx:   idx,
		tr:    tr,
		id:    id,
		hub:   c.hub,
		hubID: hubID,
	}

	cfg := dht.Config{
		RequestTimeout: c.opts.RequestTimeout,
		LookupTimeout:  c.opts.LookupTimeout,
		K:              c.opts.K,
		Alpha:          c.opts.Alpha,
		Disjoint:       c.opts.Disjoint,
	}

	// If signaling is enabled, the Service must be ready BEFORE
	// dht.NewNode so we can pass it as the dht.Extension.
	// dht.Node has no post-construction hook for this.
	if c.sig != nil {
		p.svc = c.sig.makeService(t, p)
		cfg.Extension = p.svc
	}

	p.node = dht.NewNode(id, tr, nil, cfg)

	if c.sig != nil {
		c.sig.afterNodeReady(p)
	}

	ctx, cancel := context.WithCancel(t.Context())
	p.stop = cancel

	c.mu.Lock()
	c.peers = append(c.peers, p)
	c.mu.Unlock()

	c.wg.Go(func() {
		p.node.Run(ctx)
	})

	t.Cleanup(func() {
		cancel()
		_ = tr.Close()
	})

	return p
}

// Stop tears the cluster down — cancels every peer's run context,
// closes transports, waits for goroutines. t.Cleanup also calls this
// transitively through each peer's cleanup, so most tests do not need
// to call Stop explicitly.
func (c *Cluster) Stop() {
	c.mu.RLock()
	peers := append([]*Peer(nil), c.peers...)
	c.mu.RUnlock()

	for _, p := range peers {
		p.stop()
	}
	for _, p := range peers {
		_ = p.tr.Close()
	}
	c.wg.Wait()
}

// Connect seeds an edge between peers i and j. Implemented as a
// Ping(i→j): peer i learns j synchronously through the PONG; peer j
// learns i asynchronously when the verification probe completes.
// Tests that need symmetry should WaitFor(BothKnowEachOther).
func (c *Cluster) Connect(t *testing.T, i, j int) {
	t.Helper()

	if i == j {
		t.Fatalf("topotest: Connect(%d,%d): self-loop", i, j)
	}
	pi := c.Peer(i)
	pj := c.Peer(j)

	ctx, cancel := context.WithTimeout(t.Context(), c.opts.ConnectTimeout)
	defer cancel()

	if err := pi.node.Ping(ctx, pj.tr.LocalAddr()); err != nil {
		t.Fatalf("topotest: Connect(%d→%d) ping: %v", i, j, err)
	}
}

// ConnectMany applies Connect to every (a, b) pair in edges.
func (c *Cluster) ConnectMany(t *testing.T, edges []Edge) {
	t.Helper()

	for _, e := range edges {
		c.Connect(t, e.A, e.B)
	}
}

// Bootstrap runs DHT bootstrap on peer i against peer j — i.e. peer i
// pings j and then iteratively looks itself up to populate its
// routing table. Stronger than Connect because it pulls in j's
// neighbourhood. Use this when you want realistic discovery.
func (c *Cluster) Bootstrap(t *testing.T, i, j int) {
	t.Helper()

	pi := c.Peer(i)
	pj := c.Peer(j)
	ctx, cancel := context.WithTimeout(t.Context(), c.opts.LookupTimeout)
	defer cancel()
	if err := pi.node.Bootstrap(ctx, pj.tr.LocalAddr()); err != nil {
		t.Fatalf("topotest: Bootstrap(%d→%d): %v", i, j, err)
	}
}

// LookupAcross asserts that every peer can LookupNode every other
// peer's ID via iterative FIND_NODE. Returns the worst-case lookup
// duration so tests can sanity-check it.
func (c *Cluster) LookupAcross(t *testing.T) time.Duration {
	t.Helper()

	peers := c.Peers()
	var worst time.Duration
	for i, src := range peers {
		for j, dst := range peers {
			if i == j {
				continue
			}
			start := time.Now()
			ctx, cancel := context.WithTimeout(t.Context(), c.opts.LookupTimeout)
			closest, err := src.node.LookupNode(ctx, dst.node.ID())
			cancel()
			d := time.Since(start)
			if d > worst {
				worst = d
			}
			if err != nil {
				t.Fatalf("topotest: peer %d → peer %d lookup: %v", i, j, err)
			}
			if !containsID(closest, dst.node.ID()) {
				t.Fatalf("topotest: peer %d did not find peer %d (got %d closest contacts)", i, j, len(closest))
			}
		}
	}

	return worst
}

// LookupSuccessRate runs a single lookup from src for each target
// (excluding src itself) and reports the fraction that found the
// target's ID in the result set. Used by lookup-correctness tests
// where we expect <100% success (e.g. partial topologies, lossy
// transports).
func (c *Cluster) LookupSuccessRate(t *testing.T, src int, targets []int) float64 {
	t.Helper()

	pi := c.Peer(src)
	hits := 0
	tried := 0
	for _, j := range targets {
		if j == src {
			continue
		}
		tried++
		dst := c.Peer(j)
		ctx, cancel := context.WithTimeout(t.Context(), c.opts.LookupTimeout)
		closest, err := pi.node.LookupNode(ctx, dst.node.ID())
		cancel()
		if err != nil {
			continue
		}
		if containsID(closest, dst.node.ID()) {
			hits++
		}
	}
	if tried == 0 {
		return 0
	}

	return float64(hits) / float64(tried)
}

// containsID reports whether `id` appears in `cs`.
func containsID(cs []dht.Contact, id dht.NodeID) bool {
	for _, c := range cs {
		if c.ID == id {
			return true
		}
	}

	return false
}

// WaitFor polls predicate until it returns true or the deadline
// elapses. Returns false on timeout. Use this to await routing-table
// convergence after async events (probes, partition heal, etc.).
func WaitFor(t *testing.T, timeout time.Duration, predicate func() bool) bool {
	t.Helper()

	if timeout <= 0 {
		timeout = DefaultWaitTimeout
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}

	return predicate()
}

// KnowsAbout reports whether peer i has peer j in its routing table.
func (c *Cluster) KnowsAbout(i, j int) bool {
	pi := c.Peer(i)
	pj := c.Peer(j)
	_, ok := pi.node.Table().Contact(pj.node.ID())

	return ok
}

// AllPairsKnow reports whether every peer in idx has every other
// peer in idx in its routing table.
func (c *Cluster) AllPairsKnow(idx []int) bool {
	for _, i := range idx {
		for _, j := range idx {
			if i == j {
				continue
			}
			if !c.KnowsAbout(i, j) {
				return false
			}
		}
	}

	return true
}

// Detach removes peer i from the hub so its transport stops receiving
// new packets. Returns the previous registration so the test can put
// it back via Reattach. Used to simulate node-leave / partition.
func (c *Cluster) Detach(i int) *transport.MemoryTransport {
	p := c.Peer(i)

	return c.hub.Swap(p.hubID, nil)
}

// Reattach restores a previously-detached transport. Pass the value
// returned from Detach.
func (c *Cluster) Reattach(i int, prev *transport.MemoryTransport) {
	if prev == nil {
		return
	}
	p := c.Peer(i)
	c.hub.Swap(p.hubID, prev)
}

// Shuffle returns a deterministic permutation of [0, n) using r.
func Shuffle(r *mathrand.Rand, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	r.Shuffle(n, func(i, j int) { out[i], out[j] = out[j], out[i] })

	return out
}

// MustNoError fails the test if err is non-nil with context.
func MustNoError(t *testing.T, err error, format string, args ...any) {
	t.Helper()

	if err != nil {
		t.Fatalf(format+": %v", append(args, err)...)
	}
}

// LogStats writes a short routing-table size summary to t.Log,
// useful when a test fails and we want to see the topology shape.
func (c *Cluster) LogStats(t *testing.T) {
	t.Helper()

	peers := c.Peers()
	for i, p := range peers {
		t.Logf("peer %d: id=%s table_size=%d", i, p.node.ID().String()[:8], p.node.Table().Size())
	}
}

// Errorf is a tiny convenience for harness-internal failures.
func Errorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
