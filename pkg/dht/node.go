package dht

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/ratelimit"
	"github.com/udisondev/udisend/pkg/transport"
	"github.com/udisondev/udisend/pkg/wire"
)

// Defaults for lookup behaviour. Tunable via Config.
const (
	DefaultAlpha          = 3
	DefaultRequestTimeout = 2 * time.Second
	DefaultLookupTimeout  = 8 * time.Second
	// DefaultStoreTTL is the eviction window the local store uses for
	// values written through STORE / PutValue when no caller-provided TTL
	// is available. Higher layers (presence) decide their own record TTL
	// and pass it through Config / API.
	DefaultStoreTTL = 90 * time.Second
	// DefaultDisjoint is the number of independent lookup paths run in
	// parallel during iterativeFind. design.md §4 / S-Kademlia §4.2:
	// d=3 is the canonical paper recommendation — it caps the cost
	// of a Sybil cluster on any one lookup at 1/d, since paths are
	// disjoint by a shared visited-set guard.
	DefaultDisjoint = 3
)

// PacketHandler is invoked for inbound packets whose type is not consumed
// by the DHT itself (notably MsgRelay, used by the signaling layer).
// Returning false leaves the packet for further processing by the DHT —
// today that means it is dropped.
type PacketHandler func(ctx context.Context, pkt transport.Packet, typ byte, payload []byte) bool

// Config tunes Node behaviour.
type Config struct {
	K              int
	Alpha          int
	RequestTimeout time.Duration
	LookupTimeout  time.Duration
	Logger         *slog.Logger
	// ExtraHandler, if non-nil, is given first crack at every inbound packet
	// the DHT does not handle natively. Used by pkg/signaling to multiplex
	// MsgRelay frames over the same socket.
	ExtraHandler PacketHandler
	// InboundRate / InboundBurst configure the per-source-IP DoS limiter on
	// inbound packets (design.md §8). Zero rate disables limiting.
	InboundRate  float64
	InboundBurst float64
	// Disjoint is the number of independent paths run in parallel during
	// an iterative lookup (S-Kademlia §4.2 disjoint-paths). Initial
	// shortlist contacts are partitioned round-robin between paths, and
	// a shared visited-set guarantees that a peer touched by one path
	// is never queried by another. Zero means DefaultDisjoint.
	// design.md §4.
	Disjoint int
	// Siblings is the size of the sibling list — the s contacts closest
	// to the local node, used as additional replication targets in
	// PutValue (S-Kademlia §4.4). Zero means K. design.md §4.
	Siblings int
}

// DefaultInboundRate / DefaultInboundBurst are conservative caps suitable
// for a network node serving thousands of clients without paying for
// real DDoS mitigation infrastructure: 100 packets per second per
// source IP, with a burst tolerance of 200.
const (
	DefaultInboundRate  = 100
	DefaultInboundBurst = 200
)

func (c *Config) defaults() {
	if c.K == 0 {
		c.K = DefaultK
	}
	if c.Alpha == 0 {
		c.Alpha = DefaultAlpha
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = DefaultRequestTimeout
	}
	if c.LookupTimeout == 0 {
		c.LookupTimeout = DefaultLookupTimeout
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.InboundRate == 0 {
		c.InboundRate = DefaultInboundRate
	}
	if c.InboundBurst == 0 {
		c.InboundBurst = DefaultInboundBurst
	}
	if c.Disjoint <= 0 {
		c.Disjoint = DefaultDisjoint
	}
	if c.Siblings <= 0 {
		c.Siblings = c.K
	}
}

// Node is a participant in the Kademlia DHT. It owns a routing table, a
// local store and a transport, and exposes RPCs (Ping / FindNode / Store /
// FindValue) plus iterative lookups.
type Node struct {
	id        *identity.Identity
	transport transport.Transport
	table     *RoutingTable
	store     Store
	cfg       Config
	limiter   *ratelimit.Limiter

	pendingMu sync.Mutex
	pending   map[TxID]chan any

	closeOnce sync.Once
	closed    chan struct{}
}

// NewNode wires up a node but does not start the receive loop — call Run
// for that. Store, when nil, falls back to a bare MemoryStore which
// performs NO write validation — only suitable for tests / in-process
// experiments. Production callers in pkg/network wrap the store with
// presence.NewRateLimitedStore to reject unsigned records. The Phase 9
// audit flagged the silent default as a foot-gun; the warning makes
// the trade-off visible in operator logs.
func NewNode(id *identity.Identity, t transport.Transport, store Store, cfg Config) *Node {
	cfg.defaults()
	if store == nil {
		if cfg.Logger != nil {
			cfg.Logger.Warn("dht: NewNode called with nil Store — using unvalidated MemoryStore (insecure outside tests)")
		}
		store = NewMemoryStore(nil)
	}
	return &Node{
		id:        id,
		transport: t,
		table:     NewRoutingTable(id.Public().DestinationHash(), cfg.K),
		store:     store,
		cfg:       cfg,
		limiter:   ratelimit.New(cfg.InboundRate, cfg.InboundBurst),
		pending:   make(map[TxID]chan any),
		closed:    make(chan struct{}),
	}
}

// ID returns the local node's NodeID.
func (n *Node) ID() NodeID { return n.table.Self() }

// Identity exposes the underlying keypair (read-only).
func (n *Node) Identity() *identity.Identity { return n.id }

// Table exposes the routing table for read-only inspection.
func (n *Node) Table() *RoutingTable { return n.table }

// Transport exposes the underlying transport so adjacent layers
// (signaling) can share the same socket.
func (n *Node) Transport() transport.Transport { return n.transport }

// LocalStore exposes the value store for higher-level packages
// (presence) that need to read replicated data directly.
func (n *Node) LocalStore() Store { return n.store }

// Run starts the inbound dispatch loop. It blocks until ctx is cancelled
// or the transport closes.
func (n *Node) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.closed:
			return
		case pkt, ok := <-n.transport.Inbox():
			if !ok {
				return
			}
			n.handlePacket(ctx, pkt)
		}
	}
}

// Close stops the node and refuses subsequent RPCs.
func (n *Node) Close() {
	n.closeOnce.Do(func() { close(n.closed) })
}

func (n *Node) handlePacket(ctx context.Context, pkt transport.Packet) {
	if !n.limiter.Allow(sourceKey(pkt.From)) {
		n.cfg.Logger.Debug("dht: inbound rate-limited", "from", pkt.From)
		return
	}
	typ, body, err := wire.DecodeFrame(pkt.Payload)
	if err != nil {
		n.cfg.Logger.Debug("dht: bad frame", "from", pkt.From, "err", err)
		return
	}
	// Dispatch non-DHT frames (MsgRelay, future types) to the embedder.
	switch typ {
	case MsgPing, MsgPong, MsgFindNode, MsgNodes,
		MsgStore, MsgStoreOK, MsgFindValue, MsgValue:
	default:
		if n.cfg.ExtraHandler != nil && n.cfg.ExtraHandler(ctx, pkt, typ, body) {
			return
		}
		return
	}

	msg, err := DecodeMsg(pkt.Payload)
	if err != nil {
		n.cfg.Logger.Debug("dht: decode failed", "err", err)
		return
	}
	// Phase 9: routing-table inserts are bounded by per-/24-prefix cap
	// (bucket.add → subnetOver) so a single attacker IP cannot fill a
	// bucket with arbitrary NodeIDs. We still refresh on both request
	// and response paths because dropping requests-side adds breaks
	// Kademlia convergence (a fresh peer otherwise never appears in
	// the routing table of an unresponsive bootstrap node).
	switch m := msg.(type) {
	case *PingMsg:
		n.refreshContact(m.Header, pkt.From)
		_ = n.sendMsg(ctx, pkt.From, &PongMsg{Header: n.replyHeader(m.Header.TxID)})
	case *PongMsg:
		n.refreshContact(m.Header, pkt.From)
		n.deliver(m.Header.TxID, m)
	case *FindNodeMsg:
		n.refreshContact(m.Header, pkt.From)
		closest := n.table.Closest(m.Target, n.cfg.K)
		_ = n.sendMsg(ctx, pkt.From, &NodesMsg{
			Header:   n.replyHeader(m.Header.TxID),
			Contacts: encodeContacts(closest),
		})
	case *NodesMsg:
		n.refreshContact(m.Header, pkt.From)
		n.deliver(m.Header.TxID, m)
	case *StoreMsg:
		n.refreshContact(m.Header, pkt.From)
		if ss, ok := n.store.(SourcedStore); ok {
			ss.PutFromSource(m.Key, m.Value, DefaultStoreTTL, pkt.From)
		} else {
			n.store.Put(m.Key, m.Value, DefaultStoreTTL)
		}
		_ = n.sendMsg(ctx, pkt.From, &StoreOKMsg{Header: n.replyHeader(m.Header.TxID)})
	case *StoreOKMsg:
		n.refreshContact(m.Header, pkt.From)
		n.deliver(m.Header.TxID, m)
	case *FindValueMsg:
		n.refreshContact(m.Header, pkt.From)
		if val, ok := n.store.Get(m.Key); ok {
			_ = n.sendMsg(ctx, pkt.From, &ValueMsg{
				Header: n.replyHeader(m.Header.TxID),
				Value:  val,
			})
		} else {
			closest := n.table.Closest(m.Key, n.cfg.K)
			_ = n.sendMsg(ctx, pkt.From, &NodesMsg{
				Header:   n.replyHeader(m.Header.TxID),
				Contacts: encodeContacts(closest),
			})
		}
	case *ValueMsg:
		n.refreshContact(m.Header, pkt.From)
		n.deliver(m.Header.TxID, m)
	}
}

func (n *Node) refreshContact(h Header, from net.Addr) {
	if h.SrcID == n.ID() {
		return
	}
	addr := from
	// Trust the network-observed source address (from); the SrcAddr field
	// is advisory and could be spoofed/wrong by NAT. Only fall back to
	// SrcAddr if `from` is missing (in-process tests sometimes elide it).
	if addr == nil && h.SrcAddr != "" {
		if resolved, err := n.transport.Dial(h.SrcAddr); err == nil {
			addr = resolved
		}
	}
	if addr == nil {
		return
	}
	n.table.Add(Contact{ID: h.SrcID, Addr: addr, LastSeen: time.Now()})
}

func (n *Node) replyHeader(tx TxID) Header {
	return Header{
		TxID:    tx,
		SrcID:   n.ID(),
		SrcAddr: n.transport.LocalAddr().String(),
	}
}

func (n *Node) newRequestHeader() Header {
	return Header{
		TxID:    NewTxID(),
		SrcID:   n.ID(),
		SrcAddr: n.transport.LocalAddr().String(),
	}
}

func (n *Node) sendMsg(ctx context.Context, to net.Addr, m any) error {
	blob, err := EncodeMsg(m)
	if err != nil {
		return err
	}
	return n.transport.Send(ctx, to, blob)
}

func (n *Node) register(tx TxID) chan any {
	ch := make(chan any, 1)
	n.pendingMu.Lock()
	n.pending[tx] = ch
	n.pendingMu.Unlock()
	return ch
}

func (n *Node) cancel(tx TxID) {
	n.pendingMu.Lock()
	delete(n.pending, tx)
	n.pendingMu.Unlock()
}

func (n *Node) deliver(tx TxID, msg any) {
	n.pendingMu.Lock()
	ch, ok := n.pending[tx]
	delete(n.pending, tx)
	n.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

// Ping sends a ping and waits for a pong.
func (n *Node) Ping(ctx context.Context, addr net.Addr) error {
	hdr := n.newRequestHeader()
	ch := n.register(hdr.TxID)
	defer n.cancel(hdr.TxID)
	if err := n.sendMsg(ctx, addr, &PingMsg{Header: hdr}); err != nil {
		return err
	}
	return n.wait(ctx, ch, n.cfg.RequestTimeout)
}

// FindNode asks `addr` for the K closest contacts to target.
func (n *Node) FindNode(ctx context.Context, addr net.Addr, target NodeID) ([]Contact, error) {
	hdr := n.newRequestHeader()
	ch := n.register(hdr.TxID)
	defer n.cancel(hdr.TxID)
	if err := n.sendMsg(ctx, addr, &FindNodeMsg{Header: hdr, Target: target}); err != nil {
		return nil, err
	}
	resp, err := n.waitFor(ctx, ch, n.cfg.RequestTimeout)
	if err != nil {
		return nil, err
	}
	nodes, ok := resp.(*NodesMsg)
	if !ok {
		return nil, fmt.Errorf("dht: expected NodesMsg, got %T", resp)
	}
	return n.decodeContacts(nodes.Contacts), nil
}

// FindValue queries `addr` for a stored value. If the peer doesn't have
// it, returns (nil, contacts, nil) with the closest contacts.
func (n *Node) FindValue(ctx context.Context, addr net.Addr, key NodeID) ([]byte, []Contact, error) {
	hdr := n.newRequestHeader()
	ch := n.register(hdr.TxID)
	defer n.cancel(hdr.TxID)
	if err := n.sendMsg(ctx, addr, &FindValueMsg{Header: hdr, Key: key}); err != nil {
		return nil, nil, err
	}
	resp, err := n.waitFor(ctx, ch, n.cfg.RequestTimeout)
	if err != nil {
		return nil, nil, err
	}
	switch m := resp.(type) {
	case *ValueMsg:
		return m.Value, nil, nil
	case *NodesMsg:
		return nil, n.decodeContacts(m.Contacts), nil
	default:
		return nil, nil, fmt.Errorf("dht: unexpected reply %T", resp)
	}
}

// Store asks `addr` to store key=value.
func (n *Node) Store(ctx context.Context, addr net.Addr, key NodeID, value []byte) error {
	hdr := n.newRequestHeader()
	ch := n.register(hdr.TxID)
	defer n.cancel(hdr.TxID)
	if err := n.sendMsg(ctx, addr, &StoreMsg{Header: hdr, Key: key, Value: value}); err != nil {
		return err
	}
	_, err := n.waitFor(ctx, ch, n.cfg.RequestTimeout)
	return err
}

func (n *Node) wait(ctx context.Context, ch <-chan any, timeout time.Duration) error {
	_, err := n.waitFor(ctx, ch, timeout)
	return err
}

func (n *Node) waitFor(ctx context.Context, ch <-chan any, timeout time.Duration) (any, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case msg := <-ch:
		return msg, nil
	case <-t.C:
		return nil, errRequestTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-n.closed:
		return nil, errors.New("dht: node closed")
	}
}

var errRequestTimeout = errors.New("dht: request timeout")

// Bootstrap pings a known peer, then runs FindNode on self to fill the
// routing table. Idempotent.
func (n *Node) Bootstrap(ctx context.Context, peer net.Addr) error {
	if err := n.Ping(ctx, peer); err != nil {
		return fmt.Errorf("dht: bootstrap ping: %w", err)
	}
	_, err := n.LookupNode(ctx, n.ID())
	return err
}

// LookupNode runs an iterative FIND_NODE for target, returning the K
// closest contacts found.
func (n *Node) LookupNode(ctx context.Context, target NodeID) ([]Contact, error) {
	return n.iterativeFind(ctx, target, false, nil)
}

// LookupValue runs an iterative FIND_VALUE for key. Returns (value, nil)
// on success or (nil, contacts) if no peer holds the value.
func (n *Node) LookupValue(ctx context.Context, key NodeID) ([]byte, []Contact, error) {
	return n.iterativeFindValue(ctx, key)
}

// PutValue stores key=value on the K closest known peers AND on the
// local sibling list, in parallel. design.md §4 / S-Kademlia §4.4: the
// sibling replicas keep the record alive even if a Sybil cluster
// captures the K closest peers to `key` — their data is duplicated
// onto our own neighbourhood, which the attacker would have to capture
// independently.
func (n *Node) PutValue(ctx context.Context, key NodeID, value []byte) error {
	closest, err := n.LookupNode(ctx, key)
	if err != nil {
		return err
	}
	siblings := n.table.Siblings(n.cfg.Siblings)
	targets := mergeContacts(closest, siblings)
	if len(targets) == 0 {
		// Single-node network: store locally and call it a day.
		n.store.Put(key, value, DefaultStoreTTL)
		return nil
	}

	// Bounded fan-out: K=20 closest + Siblings=20 = up to 40 concurrent
	// outbound STOREs without a cap. A misbehaving target cluster can
	// keep them all blocked on RequestTimeout. PutValueParallelism
	// caps in-flight at 8, which is plenty since any single success
	// satisfies the "value is stored somewhere reachable" contract.
	const putValueParallelism = 8
	var wg sync.WaitGroup
	errs := make([]error, len(targets))
	sem := make(chan struct{}, putValueParallelism)
	for i, c := range targets {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			storeCtx, cancel := context.WithTimeout(ctx, n.cfg.RequestTimeout)
			defer cancel()
			if err := n.Store(storeCtx, c.Addr, key, value); err != nil {
				errs[i] = err
			}
		})
	}
	wg.Wait()
	// Always cache locally too.
	n.store.Put(key, value, DefaultStoreTTL)
	for _, e := range errs {
		if e == nil {
			return nil // any one success is enough
		}
	}
	return errors.Join(errs...)
}

// mergeContacts returns the deduplicated union of two contact slices.
// Order is preserved from `a` first, then any new entries from `b`.
func mergeContacts(a, b []Contact) []Contact {
	if len(b) == 0 {
		return a
	}
	if len(a) == 0 {
		return b
	}
	seen := make(map[NodeID]bool, len(a)+len(b))
	out := make([]Contact, 0, len(a)+len(b))
	for _, c := range a {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	for _, c := range b {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		out = append(out, c)
	}

	return out
}

// iterativeFind runs a Kademlia FIND_NODE / FIND_VALUE lookup over
// Config.Disjoint independent paths in parallel (S-Kademlia §4.2): a
// single shared visited-set ensures no peer is queried by more than
// one path, and the initial shortlist is partitioned round-robin
// between paths so every path explores a disjoint slice of the
// network.
//
// Result is the union of all paths' shortlists, sorted by XOR distance,
// truncated to K.
func (n *Node) iterativeFind(
	ctx context.Context,
	target NodeID,
	wantValue bool,
	maybeValue *[]byte,
) ([]Contact, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, n.cfg.LookupTimeout)
	defer cancel()

	initial := n.table.Closest(target, n.cfg.K)
	if len(initial) == 0 {
		return nil, nil
	}

	d := min(n.cfg.Disjoint, len(initial))

	paths := make([]*pathState, d)
	for i := range paths {
		paths[i] = &pathState{queried: make(map[NodeID]bool)}
	}

	// Round-robin partition initial seeds across paths so each path
	// starts from a disjoint slice of the local routing table.
	for i, c := range initial {
		paths[i%d].shortlist = append(paths[i%d].shortlist, c)
	}

	var visitedMu sync.Mutex
	visited := make(map[NodeID]bool)

	pathCtx, cancelPaths := context.WithCancel(timeoutCtx)
	defer cancelPaths()

	valueCh := make(chan valueHit, d)

	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Go(func() {
			n.runPath(pathCtx, target, wantValue, p, &visitedMu, visited, valueCh)
		})
	}

	if wantValue {
		// First path that finds the value wins — cancel the rest.
		select {
		case hit := <-valueCh:
			cancelPaths()
			wg.Wait()
			if maybeValue != nil {
				*maybeValue = hit.value
			}
			return mergePathShortlists(paths, target, n.cfg.K), nil
		case <-pathCtx.Done():
			wg.Wait()
			return mergePathShortlists(paths, target, n.cfg.K), nil
		}
	}

	wg.Wait()
	return mergePathShortlists(paths, target, n.cfg.K), nil
}

// pathState holds the per-disjoint-path local state: the shortlist of
// best-known contacts (sorted by XOR distance to the target, capped at
// K) and the set of contacts this particular path has already queried.
type pathState struct {
	mu        sync.Mutex
	shortlist []Contact
	queried   map[NodeID]bool
}

// valueHit carries a found value from a disjoint path back to the
// lookup driver. The driver cancels remaining paths once any one of
// them sends.
type valueHit struct {
	value []byte
}

// runPath drives a single disjoint lookup path until it can make no
// further progress (no unqueried contacts in its private shortlist that
// the global visited-set has not already claimed).
func (n *Node) runPath(
	ctx context.Context,
	target NodeID,
	wantValue bool,
	p *pathState,
	visitedMu *sync.Mutex,
	visited map[NodeID]bool,
	valueCh chan<- valueHit,
) {
	for {
		if ctx.Err() != nil {
			return
		}
		batch := n.claimBatch(p, visitedMu, visited)
		if len(batch) == 0 {
			return
		}

		type result struct {
			contacts []Contact
			value    []byte
		}
		results := make(chan result, len(batch))
		for _, c := range batch {
			go func() {
				if wantValue {
					val, contacts, err := n.FindValue(ctx, c.Addr, target)
					if err != nil {
						results <- result{}
						return
					}
					results <- result{contacts: contacts, value: val}
					return
				}
				contacts, err := n.FindNode(ctx, c.Addr, target)
				if err != nil {
					results <- result{}
					return
				}
				results <- result{contacts: contacts}
			}()
		}

		for range batch {
			r := <-results
			if r.value != nil {
				select {
				case valueCh <- valueHit{value: r.value}:
				default:
				}
				return
			}
			p.mu.Lock()
			for _, nc := range r.contacts {
				p.shortlist = mergeShortlist(p.shortlist, nc, target, n.cfg.K)
			}
			p.mu.Unlock()
		}
	}
}

// claimBatch atomically picks up to alpha contacts from the path's
// shortlist that are neither queried by this path nor visited by any
// other path, and marks them claimed in both sets. The visited-set
// guard is what enforces disjointness (S-Kademlia §4.2): two paths can
// never query the same peer.
func (n *Node) claimBatch(p *pathState, visitedMu *sync.Mutex, visited map[NodeID]bool) []Contact {
	p.mu.Lock()
	defer p.mu.Unlock()

	visitedMu.Lock()
	defer visitedMu.Unlock()

	var batch []Contact
	for _, c := range p.shortlist {
		if p.queried[c.ID] || visited[c.ID] {
			continue
		}
		batch = append(batch, c)
		visited[c.ID] = true
		p.queried[c.ID] = true
		if len(batch) >= n.cfg.Alpha {
			break
		}
	}

	return batch
}

// mergePathShortlists combines the per-path shortlists into one final
// list of K contacts closest to the target, deduplicated by ID.
func mergePathShortlists(paths []*pathState, target NodeID, k int) []Contact {
	combined := make([]Contact, 0, len(paths)*k)
	seen := make(map[NodeID]bool)
	for _, p := range paths {
		p.mu.Lock()
		for _, c := range p.shortlist {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			combined = append(combined, c)
		}
		p.mu.Unlock()
	}
	slices.SortFunc(combined, func(a, b Contact) int {
		return distanceCompare(a.ID, b.ID, target)
	})
	if len(combined) > k {
		combined = combined[:k]
	}

	return combined
}

func (n *Node) iterativeFindValue(ctx context.Context, key NodeID) ([]byte, []Contact, error) {
	if val, ok := n.store.Get(key); ok {
		return val, nil, nil
	}
	var found []byte
	contacts, err := n.iterativeFind(ctx, key, true, &found)
	if found != nil {
		// Cache the value locally for the rest of its TTL so re-lookups are cheap.
		n.store.Put(key, found, DefaultStoreTTL)
		return found, nil, nil
	}
	return nil, contacts, err
}

func mergeShortlist(list []Contact, c Contact, target NodeID, k int) []Contact {
	if slices.ContainsFunc(list, func(e Contact) bool { return e.ID == c.ID }) {
		return list
	}
	list = append(list, c)
	slices.SortFunc(list, func(a, b Contact) int {
		return distanceCompare(a.ID, b.ID, target)
	})
	if len(list) > k {
		list = list[:k]
	}

	return list
}

// sourceKey returns the rate-limit bucket key for a packet origin: host
// portion of host:port for routable transports, raw addr otherwise.
//
// For *net.UDPAddr (the production hot path) we key on the raw IP bytes
// converted to a string rather than the formatted IP literal — this is
// one stdlib-intrinsic 4- or 16-byte allocation instead of an
// IP.String() formatter pass that emits 12+ characters per IPv4. Map
// equality semantics are unchanged since two equal []byte slices produce
// equal Go strings.
func sourceKey(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	if u, ok := addr.(*net.UDPAddr); ok && len(u.IP) > 0 {
		return string(u.IP)
	}
	s := addr.String()
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

func encodeContacts(cs []Contact) []EncodedContact {
	out := make([]EncodedContact, len(cs))
	for i, c := range cs {
		out[i] = EncodedContact{ID: c.ID, Addr: c.Addr.String()}
	}
	return out
}

// decodeContacts turns wire EncodedContacts into runtime Contacts.
//
// Phase 9 audit fix: the per-/24 cap is applied to the RETURNED slice
// as well as the routing table. Without this, a hostile peer can put
// 20 attacker-chosen NodeIDs all on one /24 into a single NODES
// response and force iterativeFind to fan out 20 outbound FIND_NODEs
// — closing the routing-table-insert side without closing the
// iterative-driver amplifier. Returning at most MaxContactsPerSubnet
// per /24 group bounds the fan-out symmetrically with bucket.add.
func (n *Node) decodeContacts(cs []EncodedContact) []Contact {
	out := make([]Contact, 0, len(cs))
	subnetCount := make(map[string]int, len(cs))
	for _, ec := range cs {
		addr, err := n.transport.Dial(ec.Addr)
		if err != nil {
			continue
		}
		key := addrSubnet(addr)
		if key != "" && subnetCount[key] >= MaxContactsPerSubnet {
			continue
		}
		if key != "" {
			subnetCount[key]++
		}
		c := Contact{ID: ec.ID, Addr: addr, LastSeen: time.Now()}
		out = append(out, c)
		n.table.Add(c)
	}
	return out
}
