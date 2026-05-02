// Package messenger glues identity, dht, presence, signaling and storage
// into a thin runtime that the browser-side UI consumes through
// internal/httpui. The runtime owns:
//
//   - Cryptographic identity and X25519 ECDH for Noise.
//   - Local DHT participation and presence publishing.
//   - The signaling.Service that delivers encrypted byte streams to peers.
//   - SQLite storage for contacts, message history and outbox.
//
// What it does NOT own (the v2 split):
//
//   - WebRTC PeerConnection — that lives in the browser, where native
//     RTCPeerConnection handles media, codecs, jitter buffers, echo
//     cancellation and <video> rendering. The Go side simply ferries
//     SignedSDP envelopes between two browsers through the
//     end-to-end-encrypted signaling pipe.
//   - Application-level chat framing — the browser owns DataChannel
//     payloads. Storage methods exist so the browser can persist sent
//     and received messages, but the runtime does not parse them.
package messenger

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/transport"
)

// Config configures Open.
type Config struct {
	Identity    *identity.Identity
	Bootstrap   []string
	Listen      string // UDP listen address
	StorageDir  string
	PresenceTTL time.Duration
	// OutboxInterval controls how often the runtime re-checks recipients of
	// pending outbox items; on each tick recipients whose presence is
	// resolvable get a "peer_online" notification so the UI can drain. Zero
	// uses the default (30 s); negative disables the pump entirely.
	OutboxInterval time.Duration
	Logger         *slog.Logger
}

// Messenger is the per-user runtime.
type Messenger struct {
	cfg Config

	id        *identity.Identity
	transport *transport.UDPTransport
	node      *dht.Node
	publisher *presence.Publisher
	resolver  *presence.Resolver
	signaling *signaling.Service
	storage   *storage.Store

	sessionsMu sync.Mutex
	sessions   map[sessionKey]*Session

	incomingMu sync.RWMutex
	incoming   func(*Session)

	peerOnlineMu sync.RWMutex
	peerOnline   func(identity.Hash)

	closeOnce sync.Once
}

// DefaultOutboxInterval is the period between outbox flush ticks when
// Config.OutboxInterval is zero.
const DefaultOutboxInterval = 30 * time.Second

type sessionKey struct {
	peer identity.Hash
	sid  string
}

// Open spins the runtime up.
func Open(ctx context.Context, cfg Config) (*Messenger, error) {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.PresenceTTL == 0 {
		cfg.PresenceTTL = presence.DefaultRecordTTL
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.StorageDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = "."
		}
		cfg.StorageDir = filepath.Join(dir, "udisend", cfg.Identity.Public().DestinationHash().String())
	}
	if err := os.MkdirAll(cfg.StorageDir, 0o755); err != nil {
		return nil, fmt.Errorf("messenger: storage dir: %w", err)
	}

	tr, err := transport.ListenUDP(cfg.Listen)
	if err != nil {
		return nil, err
	}
	store, err := storage.Open(ctx, filepath.Join(cfg.StorageDir, "messenger.db"))
	if err != nil {
		_ = tr.Close()
		return nil, err
	}

	// design.md §7-8: if no CLI --bootstrap, seed from previously-seen
	// peers — preferring subnet-diverse entries so a Sybil cluster
	// colocated in one /24 cannot eclipse our routing-table from cache.
	if len(cfg.Bootstrap) == 0 {
		if cached, err := store.SeenPeersDiverse(ctx, 50); err == nil && len(cached) > 0 {
			cfg.Bootstrap = cached
			cfg.Logger.Info("messenger: bootstrap from cache", "n", len(cached))
		}
	}

	m := &Messenger{
		cfg:       cfg,
		id:        cfg.Identity,
		transport: tr,
		storage:   store,
		sessions:  make(map[sessionKey]*Session),
	}

	sig := signaling.NewService(signaling.Config{
		Identity:  cfg.Identity,
		Transport: tr,
		Resolver:  &resolverDelegate{m: m},
		Logger:    cfg.Logger,
	})
	m.signaling = sig
	sig.SetHandler(m.onIncomingChannel)

	node := dht.NewNode(cfg.Identity, tr, nil, dht.Config{
		Logger:       cfg.Logger,
		ExtraHandler: sig.HandlePacket,
	})
	m.node = node
	sig.SetRouter(dhtRouter{node: node, timeout: messengerPathLookupTimeout})

	caps := presence.CapPublicIP // every messenger advertises its address
	m.resolver = presence.NewResolver(nodeAdapter{node}, cfg.PresenceTTL)
	m.publisher = presence.NewPublisher(presence.PublisherConfig{
		Identity:     cfg.Identity,
		Address:      tr.LocalAddr().String(),
		Capabilities: caps,
		TTL:          cfg.PresenceTTL,
		DHT:          nodeAdapter{node},
		Logger:       cfg.Logger,
	})

	return m, nil
}

// LocalAddress returns the externally-reachable transport address.
func (m *Messenger) LocalAddress() string { return m.transport.LocalAddr().String() }

// Identity returns the local identity.
func (m *Messenger) Identity() *identity.Identity { return m.id }

// Storage exposes the SQLite store for the UI layer.
func (m *Messenger) Storage() *storage.Store { return m.storage }

// Resolver exposes presence lookups.
func (m *Messenger) Resolver() *presence.Resolver { return m.resolver }

// Logger returns the configured slog.Logger.
func (m *Messenger) Logger() *slog.Logger { return m.cfg.Logger }

// SetIncomingHandler registers a callback for inbound signaling sessions
// initiated by remote peers. Replaces any prior handler.
func (m *Messenger) SetIncomingHandler(fn func(*Session)) {
	m.incomingMu.Lock()
	defer m.incomingMu.Unlock()
	m.incoming = fn
}

// SetPeerOnlineHandler registers a callback fired by the outbox flush pump
// each time a recipient with pending items becomes resolvable in presence.
// The UI uses this to wake up its delivery flow over DataChannel.
func (m *Messenger) SetPeerOnlineHandler(fn func(identity.Hash)) {
	m.peerOnlineMu.Lock()
	defer m.peerOnlineMu.Unlock()
	m.peerOnline = fn
}

func (m *Messenger) firePeerOnline(peer identity.Hash) {
	m.peerOnlineMu.RLock()
	fn := m.peerOnline
	m.peerOnlineMu.RUnlock()
	if fn != nil {
		fn(peer)
	}
}

// Run starts the background loops. Order matters here:
//
//  1. The DHT receive loop must run first — bootstrap responses
//     (PONG, NODES) arrive via that loop.
//  2. Bootstrap is awaited synchronously (with a timeout). Without this
//     the publisher's first PutValue races with bootstrap: an empty
//     routing table at PutValue time means the record is stored ONLY
//     locally, and remote peers cannot find us until the next refresh
//     (TTL/2 = 45s by default). That manifests as "record not found"
//     when a peer tries to add us as a contact right after we start.
//  3. Publisher starts last so its first publish reaches the live
//     bootstrap peer.
func (m *Messenger) Run(ctx context.Context) {
	var wg sync.WaitGroup

	wg.Go(func() { m.node.Run(ctx) })

	if len(m.cfg.Bootstrap) > 0 {
		m.bootstrapAll(ctx)
	}

	wg.Go(func() { m.publisher.Run(ctx) })

	if interval := m.outboxInterval(); interval > 0 {
		wg.Go(func() { m.outboxPump(ctx, interval) })
	}

	<-ctx.Done()
	wg.Wait()
}

func (m *Messenger) outboxInterval() time.Duration {
	switch {
	case m.cfg.OutboxInterval < 0:
		return 0
	case m.cfg.OutboxInterval == 0:
		return DefaultOutboxInterval
	default:
		return m.cfg.OutboxInterval
	}
}

// outboxPump periodically calls FlushOutboxOnce. Exits on ctx cancel.
func (m *Messenger) outboxPump(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.FlushOutboxOnce(ctx)
		}
	}
}

// FlushOutboxOnce iterates contacts with pending outbox items, resolves
// their presence, and fires the peer-online handler for those reachable.
// Browser-side delivery (over DataChannel) is the UI's job; the runtime
// only signals that the recipient is now visible. Exposed for tests.
func (m *Messenger) FlushOutboxOnce(ctx context.Context) {
	contacts, err := m.storage.ListContacts(ctx)
	if err != nil {
		m.cfg.Logger.Debug("messenger: outbox flush: list contacts", "err", err)
		return
	}
	for _, c := range contacts {
		select {
		case <-ctx.Done():
			return
		default:
		}
		items, err := m.storage.PendingForPeer(ctx, c.Hash)
		if err != nil || len(items) == 0 {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, lookupErr := m.resolver.Lookup(rctx, c.Hash)
		cancel()
		if lookupErr != nil {
			continue
		}
		m.firePeerOnline(c.Hash)
	}
}

// bootstrapAll dials every configured bootstrap peer in parallel and
// waits up to 5 seconds for all of them to either succeed or fail.
// Failures are logged, not returned — a single reachable peer is enough.
func (m *Messenger) bootstrapAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, addr := range m.cfg.Bootstrap {
		wg.Go(func() {
			netAddr, err := m.transport.Dial(addr)
			if err != nil {
				m.cfg.Logger.Warn("messenger: bootstrap parse", "addr", addr, "err", err)
				return
			}
			bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := m.node.Bootstrap(bctx, netAddr); err != nil {
				m.cfg.Logger.Warn("messenger: bootstrap", "addr", addr, "err", err)
				if rmErr := m.storage.ForgetSeenPeer(ctx, addr); rmErr != nil {
					m.cfg.Logger.Debug("messenger: forget seen peer", "addr", addr, "err", rmErr)
				}
				return
			}
			m.cfg.Logger.Info("messenger: bootstrap ok", "addr", addr)
			if recErr := m.storage.RecordSeenPeer(ctx, addr); recErr != nil {
				m.cfg.Logger.Debug("messenger: record seen peer", "addr", addr, "err", recErr)
			}
		})
	}
	wg.Wait()
}

// Close releases resources.
func (m *Messenger) Close() {
	m.closeOnce.Do(func() {
		// Snapshot before shutting down sessions — sess.shutdown calls
		// removeSession which re-acquires m.sessionsMu (would self-deadlock).
		m.sessionsMu.Lock()
		toShutdown := make([]*Session, 0, len(m.sessions))
		for _, sess := range m.sessions {
			toShutdown = append(toShutdown, sess)
		}
		m.sessions = make(map[sessionKey]*Session)
		m.sessionsMu.Unlock()
		for _, sess := range toShutdown {
			sess.shutdown()
		}
		m.signaling.Close()
		_ = m.transport.Close()
		_ = m.storage.Close()
	})
}

// Connect opens a signaling session to peer (initiator side). Returns a
// Session that the UI layer drives — sending offers and receiving answers
// + ICE through it.
func (m *Messenger) Connect(ctx context.Context, peer identity.Hash) (*Session, error) {
	pubID, err := m.lookupPeerPublic(ctx, peer)
	if err != nil {
		return nil, err
	}
	ch, err := m.signaling.Connect(ctx, peer)
	if err != nil {
		return nil, err
	}
	sess := m.installSession(peer, ch, pubID)
	go sess.recvLoop()
	return sess, nil
}

func (m *Messenger) onIncomingChannel(peer identity.Hash, ch *signaling.Channel) {
	pubID, err := m.lookupPeerPublic(context.Background(), peer)
	if err != nil {
		m.cfg.Logger.Warn("messenger: incoming peer pubkey", "peer", peer, "err", err)
		_ = ch.Close()
		return
	}
	sess := m.installSession(peer, ch, pubID)
	go sess.recvLoop()
	m.incomingMu.RLock()
	handler := m.incoming
	m.incomingMu.RUnlock()
	if handler != nil {
		go handler(sess)
	} else {
		m.cfg.Logger.Warn("messenger: incoming session with no handler; closing", "peer", peer)
		_ = sess.Close()
	}
}

func (m *Messenger) installSession(peer identity.Hash, ch *signaling.Channel, peerPub identity.PublicIdentity) *Session {
	sid := sessionIDFromChannel(ch)
	s := &Session{
		Peer:      peer,
		SessionID: sid,
		channel:   ch,
		messenger: m,
		peerPub:   peerPub,
		inbox:     make(chan SignalEvent, 32),
		closed:    make(chan struct{}),
	}
	m.sessionsMu.Lock()
	if old, ok := m.sessions[sessionKey{peer: peer, sid: sid}]; ok {
		old.shutdown()
	}
	m.sessions[sessionKey{peer: peer, sid: sid}] = s
	m.sessionsMu.Unlock()
	return s
}

func (m *Messenger) removeSession(peer identity.Hash, sid string) {
	m.sessionsMu.Lock()
	delete(m.sessions, sessionKey{peer: peer, sid: sid})
	m.sessionsMu.Unlock()
}

// lookupPeerPublic resolves a peer's public identity from local storage
// first, then from a DHT presence lookup.
func (m *Messenger) lookupPeerPublic(ctx context.Context, peer identity.Hash) (identity.PublicIdentity, error) {
	if c, err := m.storage.GetContact(ctx, peer); err == nil && len(c.Public.EdPub) > 0 {
		return c.Public, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	rec, err := m.resolver.Lookup(rctx, peer)
	if err != nil {
		return identity.PublicIdentity{}, err
	}
	return rec.Public, nil
}

// sessionIDFromChannel returns the canonical hex form of the channel's
// signaling.SessionID — the UI uses this string as a stable handle.
func sessionIDFromChannel(ch *signaling.Channel) string {
	sid := ch.SessionID()
	return hex.EncodeToString(sid[:])
}

// nodeAdapter / resolverDelegate identical to v1.
type nodeAdapter struct{ n *dht.Node }

func (a nodeAdapter) PutValue(ctx context.Context, key dht.NodeID, value []byte) error {
	return a.n.PutValue(ctx, key, value)
}

func (a nodeAdapter) LookupValue(ctx context.Context, key dht.NodeID) ([]byte, []dht.Contact, error) {
	return a.n.LookupValue(ctx, key)
}

// dhtRouter satisfies signaling.Router by delegating to the routing table,
// with a bounded iterative-lookup fallback (design.md §4 path requests).
type dhtRouter struct {
	node    *dht.Node
	timeout time.Duration
}

const messengerPathLookupTimeout = 1500 * time.Millisecond

func (r dhtRouter) NextHop(ctx context.Context, target identity.Hash) (net.Addr, bool) {
	closest := r.node.Table().Closest(target, 1)
	if len(closest) > 0 && closest[0].ID == target {
		return closest[0].Addr, true
	}
	lctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if found, err := r.node.LookupNode(lctx, target); err == nil {
		for _, c := range found {
			if c.ID == target {
				return c.Addr, true
			}
		}
		if len(found) > 0 {
			return found[0].Addr, true
		}
	}
	if len(closest) > 0 {
		return closest[0].Addr, true
	}
	return nil, false
}

type resolverDelegate struct{ m *Messenger }

func (r *resolverDelegate) Lookup(ctx context.Context, peer identity.Hash) (*presence.Record, error) {
	return r.m.resolver.Lookup(ctx, peer)
}

// Errors surfaced to UI callers.
var (
	ErrPeerUnknown = errors.New("messenger: peer unknown")
)
