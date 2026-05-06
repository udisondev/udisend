package topotest

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
	"github.com/udisondev/udisend/pkg/signaling"
)

// SigCluster is the signaling-layer companion of a Cluster created
// with Options.WithSignaling = true. It owns the shared static
// AddressResolver and tracks per-peer signaling.Service instances.
//
// The resolver maps peer hash → presence.Record carrying that peer's
// MemoryAddr. Tests that want to model relay-only addressing (A
// reaches B only through some intermediate peer) override the
// resolver entry with OverrideAddress.
type SigCluster struct {
	cluster *Cluster

	mu       sync.Mutex
	resolver *staticResolver
	queues   map[int]*incomingQueue
}

func newSigCluster(c *Cluster) *SigCluster {
	return &SigCluster{
		cluster:  c,
		resolver: newStaticResolver(),
		queues:   make(map[int]*incomingQueue),
	}
}

// makeService is invoked from Cluster.spawnOne BEFORE dht.NewNode.
// The returned Service is wired as the dht.Node's ExtraHandler.
func (sc *SigCluster) makeService(t *testing.T, p *Peer) *signaling.Service {
	t.Helper()

	svc := signaling.NewService(signaling.Config{
		Identity:  p.id,
		Transport: p.tr,
		Resolver:  sc.resolver,
	})
	svc.SetRouter(&clusterRouter{cluster: sc.cluster, self: p.id.Public().DestinationHash()})

	q := &incomingQueue{}
	svc.SetHandler(func(peer identity.Hash, ch *signaling.Channel) {
		q.push(peer, ch)
	})

	sc.mu.Lock()
	sc.queues[p.idx] = q
	sc.mu.Unlock()

	t.Cleanup(func() { svc.Close() })

	// Publish self in the resolver so other peers can reach us.
	rec := makeRecord(t, p.id, p.tr.LocalAddr().String())
	sc.resolver.put(rec)

	return svc
}

// afterNodeReady is a placeholder for any post-dht.Node-construction
// wiring we may need; currently a no-op.
func (sc *SigCluster) afterNodeReady(_ *Peer) {}

// Service returns the signaling.Service bound to peer i.
func (sc *SigCluster) Service(i int) *signaling.Service {
	return sc.cluster.Peer(i).svc
}

// OverrideAddress redirects the resolver's record for peer dst to
// point at peer via's transport address. Models the case where A only
// knows B through a relay: A's Connect(B) dials via's address, and
// via's signaling.Service must have a Router that forwards the inbound
// envelope on to B.
func (sc *SigCluster) OverrideAddress(t *testing.T, dst, via int) {
	t.Helper()

	dstPeer := sc.cluster.Peer(dst)
	viaPeer := sc.cluster.Peer(via)

	rec := makeRecord(t, dstPeer.id, viaPeer.tr.LocalAddr().String())
	sc.resolver.put(rec)
}

// Connect opens a signaling session from peer src to peer dst.
// Returns the initiator-side Channel.
func (sc *SigCluster) Connect(t *testing.T, src, dst int, timeout time.Duration) *signaling.Channel {
	t.Helper()

	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	srcSvc := sc.Service(src)
	dstHash := sc.cluster.Peer(dst).id.Public().DestinationHash()

	ch, err := srcSvc.Connect(ctx, dstHash)
	if err != nil {
		t.Fatalf("topotest: signaling Connect(%d→%d): %v", src, dst, err)
	}
	t.Cleanup(func() { ch.Close() })

	return ch
}

// TryConnect is the non-fatal variant: returns the Channel and an
// error instead of failing the test on Connect failure.
func (sc *SigCluster) TryConnect(t *testing.T, src, dst int, timeout time.Duration) (*signaling.Channel, error) {
	t.Helper()

	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	srcSvc := sc.Service(src)
	dstHash := sc.cluster.Peer(dst).id.Public().DestinationHash()

	ch, err := srcSvc.Connect(ctx, dstHash)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { ch.Close() })

	return ch, nil
}

// AcceptOn waits for an inbound signaling channel on peer dst.
// Returns the responder-side Channel and the peer hash that
// initiated. Fails the test on timeout.
func (sc *SigCluster) AcceptOn(t *testing.T, dst int, timeout time.Duration) (*signaling.Channel, identity.Hash) {
	t.Helper()

	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	sc.mu.Lock()
	q := sc.queues[dst]
	sc.mu.Unlock()

	arr, ok := q.pop(t.Context(), timeout)
	if !ok {
		t.Fatalf("topotest: peer %d did not accept any session within %v", dst, timeout)
	}
	t.Cleanup(func() { arr.ch.Close() })

	return arr.ch, arr.peer
}

// SendAndReceive opens a session src→dst, sends payload from src,
// receives on dst, and returns what dst got. Channels are registered
// for cleanup via Connect/AcceptOn.
func (sc *SigCluster) SendAndReceive(t *testing.T, src, dst int, payload []byte, timeout time.Duration) []byte {
	t.Helper()

	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	chSrc := sc.Connect(t, src, dst, timeout)
	chDst, peer := sc.AcceptOn(t, dst, timeout)
	srcHash := sc.cluster.Peer(src).id.Public().DestinationHash()
	if peer != srcHash {
		t.Fatalf("topotest: peer %d accepted from %x, expected %x", dst, peer[:6], srcHash[:6])
	}

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	if err := chSrc.Send(ctx, payload); err != nil {
		t.Fatalf("topotest: peer %d send: %v", src, err)
	}
	got, err := chDst.Recv(ctx)
	if err != nil {
		t.Fatalf("topotest: peer %d recv: %v", dst, err)
	}

	return got
}

// staticResolver is a simple peer→record map used by tests.
type staticResolver struct {
	mu   sync.Mutex
	recs map[identity.Hash]*presence.Record
}

func newStaticResolver() *staticResolver {
	return &staticResolver{recs: make(map[identity.Hash]*presence.Record)}
}

func (s *staticResolver) put(rec *presence.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recs[rec.DestinationHash()] = rec
}

func (s *staticResolver) Lookup(_ context.Context, peer identity.PeerID) (*presence.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.recs[peer.Bytes()]
	if !ok {
		return nil, presence.ErrNotFound
	}

	return r, nil
}

// makeRecord builds a presence.Record signed by id with the given
// transport address.
func makeRecord(t *testing.T, id *identity.Identity, addr string) *presence.Record {
	t.Helper()

	r := &presence.Record{
		Address:  addr,
		IssuedAt: time.Now().UTC(),
	}
	if err := r.Sign(id); err != nil {
		t.Fatalf("topotest: presence.Sign: %v", err)
	}

	return r
}

// clusterRouter is a signaling.Router backed by the cluster's peer
// table. NextHop and LocalNextHop both return the direct MemoryAddr
// of the destination peer if it is registered in the cluster, used
// for relay-on-the-path forwarding tests.
type clusterRouter struct {
	cluster *Cluster
	self    identity.Hash
}

func (r *clusterRouter) NextHop(_ context.Context, target identity.Hash) (net.Addr, bool) {
	return r.LocalNextHop(target)
}

func (r *clusterRouter) LocalNextHop(target identity.Hash) (net.Addr, bool) {
	for _, p := range r.cluster.Peers() {
		if p.id.Public().DestinationHash() == target {
			return p.tr.LocalAddr(), true
		}
	}

	return nil, false
}

// incomingQueue stores pending signaling arrivals on a peer until
// the test pulls them.
type incomingQueue struct {
	mu     sync.Mutex
	q      []sigArrival
	wakers []chan struct{}
}

type sigArrival struct {
	peer identity.Hash
	ch   *signaling.Channel
}

// push appends an arrival and wakes any waiters.
func (q *incomingQueue) push(peer identity.Hash, ch *signaling.Channel) {
	q.mu.Lock()
	q.q = append(q.q, sigArrival{peer: peer, ch: ch})
	wakers := q.wakers
	q.wakers = nil
	q.mu.Unlock()

	for _, w := range wakers {
		close(w)
	}
}

// pop blocks until an arrival is available or the deadline fires.
func (q *incomingQueue) pop(ctx context.Context, timeout time.Duration) (sigArrival, bool) {
	deadline := time.Now().Add(timeout)
	for {
		q.mu.Lock()
		if len(q.q) > 0 {
			arr := q.q[0]
			q.q = q.q[1:]
			q.mu.Unlock()

			return arr, true
		}
		w := make(chan struct{})
		q.wakers = append(q.wakers, w)
		q.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return sigArrival{}, false
		}
		select {
		case <-w:
			// loop and re-check the queue
		case <-time.After(remaining):
			return sigArrival{}, false
		case <-ctx.Done():
			return sigArrival{}, false
		}
	}
}
