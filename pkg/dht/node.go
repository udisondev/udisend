package dht

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
)

// Defaults for lookup behaviour. Tunable via Config.
const (
	DefaultAlpha           = 3
	DefaultRequestTimeout  = 2 * time.Second
	DefaultLookupTimeout   = 8 * time.Second
	DefaultPresenceTTL     = 90 * time.Second
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
}

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

	pendingMu sync.Mutex
	pending   map[TxID]chan any

	closeOnce sync.Once
	closed    chan struct{}
}

// NewNode wires up a node but does not start the receive loop — call Run
// for that.
func NewNode(id *identity.Identity, t transport.Transport, store Store, cfg Config) *Node {
	cfg.defaults()
	if store == nil {
		store = NewMemoryStore(nil)
	}
	return &Node{
		id:        id,
		transport: t,
		table:     NewRoutingTable(id.Public().DestinationHash(), cfg.K),
		store:     store,
		cfg:       cfg,
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
	typ, payload, err := peekFrameType(pkt.Payload)
	if err != nil {
		n.cfg.Logger.Debug("dht: bad frame", "from", pkt.From, "err", err)
		return
	}
	// Dispatch non-DHT frames (MsgRelay, future types) to the embedder.
	switch typ {
	case MsgPing, MsgPong, MsgFindNode, MsgNodes,
		MsgStore, MsgStoreOK, MsgFindValue, MsgValue:
	default:
		if n.cfg.ExtraHandler != nil && n.cfg.ExtraHandler(ctx, pkt, typ, payload) {
			return
		}
		return
	}

	msg, err := DecodeMsg(pkt.Payload)
	if err != nil {
		n.cfg.Logger.Debug("dht: decode failed", "err", err)
		return
	}
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
		n.store.Put(m.Key, m.Value, DefaultPresenceTTL)
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

// register/await txID matching helpers.
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
	select {
	case msg := <-ch:
		return msg, nil
	case <-time.After(timeout):
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
	value, foundContacts, err := n.iterativeFindValue(ctx, key)
	return value, foundContacts, err
}

// PutValue stores key=value on the K closest known peers, in parallel.
func (n *Node) PutValue(ctx context.Context, key NodeID, value []byte) error {
	closest, err := n.LookupNode(ctx, key)
	if err != nil {
		return err
	}
	if len(closest) == 0 {
		// Single-node network: store locally and call it a day.
		n.store.Put(key, value, DefaultPresenceTTL)
		return nil
	}
	var wg sync.WaitGroup
	errs := make([]error, len(closest))
	for i, c := range closest {
		wg.Add(1)
		go func(i int, c Contact) {
			defer wg.Done()
			storeCtx, cancel := context.WithTimeout(ctx, n.cfg.RequestTimeout)
			defer cancel()
			if err := n.Store(storeCtx, c.Addr, key, value); err != nil {
				errs[i] = err
			}
		}(i, c)
	}
	wg.Wait()
	// Always cache locally too.
	n.store.Put(key, value, DefaultPresenceTTL)
	for _, e := range errs {
		if e == nil {
			return nil // any one success is enough
		}
	}
	return errors.Join(errs...)
}

func (n *Node) iterativeFind(
	ctx context.Context,
	target NodeID,
	wantValue bool,
	maybeValue *[]byte,
) ([]Contact, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, n.cfg.LookupTimeout)
	defer cancel()

	shortlist := n.table.Closest(target, n.cfg.K)
	if len(shortlist) == 0 {
		return nil, nil
	}
	queried := make(map[NodeID]bool)
	for {
		// Pick alpha unqueried peers from shortlist.
		var batch []Contact
		for _, c := range shortlist {
			if !queried[c.ID] {
				batch = append(batch, c)
				if len(batch) >= n.cfg.Alpha {
					break
				}
			}
		}
		if len(batch) == 0 {
			break
		}

		type result struct {
			contacts []Contact
			value    []byte
			from     NodeID
		}
		results := make(chan result, len(batch))
		for _, c := range batch {
			queried[c.ID] = true
			go func(c Contact) {
				if wantValue {
					val, contacts, err := n.FindValue(timeoutCtx, c.Addr, target)
					if err != nil {
						results <- result{from: c.ID}
						return
					}
					results <- result{contacts: contacts, value: val, from: c.ID}
				} else {
					contacts, err := n.FindNode(timeoutCtx, c.Addr, target)
					if err != nil {
						results <- result{from: c.ID}
						return
					}
					results <- result{contacts: contacts, from: c.ID}
				}
			}(c)
		}
		for range batch {
			r := <-results
			if r.value != nil && maybeValue != nil {
				*maybeValue = r.value
				return shortlist, nil
			}
			for _, nc := range r.contacts {
				shortlist = mergeShortlist(shortlist, nc, target, n.cfg.K)
			}
		}

		// Stop when no new closer node was added.
		if !hasUnqueried(shortlist, queried) {
			break
		}
	}
	return shortlist, nil
}

func (n *Node) iterativeFindValue(ctx context.Context, key NodeID) ([]byte, []Contact, error) {
	if val, ok := n.store.Get(key); ok {
		return val, nil, nil
	}
	var found []byte
	contacts, err := n.iterativeFind(ctx, key, true, &found)
	if found != nil {
		// Cache the value locally for the rest of its TTL so re-lookups are cheap.
		n.store.Put(key, found, DefaultPresenceTTL)
		return found, nil, nil
	}
	return nil, contacts, err
}

func mergeShortlist(list []Contact, c Contact, target NodeID, k int) []Contact {
	for _, e := range list {
		if e.ID == c.ID {
			return list
		}
	}
	list = append(list, c)
	sort.Slice(list, func(i, j int) bool {
		return Less(Distance(list[i].ID, target), Distance(list[j].ID, target))
	})
	if len(list) > k {
		list = list[:k]
	}
	return list
}

func hasUnqueried(list []Contact, queried map[NodeID]bool) bool {
	for _, c := range list {
		if !queried[c.ID] {
			return true
		}
	}
	return false
}

func encodeContacts(cs []Contact) []EncodedContact {
	out := make([]EncodedContact, 0, len(cs))
	for _, c := range cs {
		out = append(out, EncodedContact{ID: c.ID, Addr: c.Addr.String()})
	}
	return out
}

func (n *Node) decodeContacts(cs []EncodedContact) []Contact {
	out := make([]Contact, 0, len(cs))
	for _, ec := range cs {
		addr, err := n.transport.Dial(ec.Addr)
		if err != nil {
			continue
		}
		c := Contact{ID: ec.ID, Addr: addr, LastSeen: time.Now()}
		out = append(out, c)
		n.table.Add(c)
	}
	return out
}

func peekFrameType(frame []byte) (byte, []byte, error) {
	if len(frame) < 3 {
		return 0, nil, errors.New("dht: short frame")
	}
	if frame[0] != 0x01 {
		return 0, nil, fmt.Errorf("dht: unknown wire version %d", frame[0])
	}
	return frame[1], frame[2:], nil
}
