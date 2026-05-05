// Package messenger is a thin client library that consumes pkg/network
// (the wire-level p2p runtime) and pkg/identity (crypto), adds storage
// (contacts, history, outbox) and signed-SDP envelopes, and exposes a
// high-level session/contact API for UI layers in cmd/<ui-binary>.
//
// What it OWNS:
//
//   - SignedSDP envelope semantics (sign on Send, verify on Recv).
//   - Per-session inboxes that demux pkg/network.Income into per-Session
//     channels for UI consumers.
//   - SQLite-backed storage for contacts, message history and outbox.
//   - The outbox-pump that wakes the UI when an offline recipient comes
//     back online.
//
// What it does NOT own:
//
//   - DHT/transport/signaling/STUN/TURN — owned by pkg/network. Caller
//     opens a *network.Node and passes it in.
//   - WebRTC PeerConnection — that lives in the browser.
//   - Application-level chat framing — the browser owns DataChannel
//     payloads. AppendHistory persists what the browser sends/receives.
package messenger

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/network"
)

// Config configures Open.
type Config struct {
	// Network is the open p2p runtime; messenger does not open or close
	// it. The caller's main owns its lifetime.
	Network *network.Node
	// Storage is the open SQLite-backed store; same ownership rule.
	Storage *storage.Store
	// OutboxInterval controls how often the runtime re-checks recipients
	// of pending outbox items; on each tick recipients whose presence is
	// resolvable get a "peer_online" notification so the UI can drain.
	// Zero uses DefaultOutboxInterval; negative disables the pump.
	OutboxInterval time.Duration
	Logger         *slog.Logger
}

// DefaultOutboxInterval is the period between outbox flush ticks when
// Config.OutboxInterval is zero.
const DefaultOutboxInterval = 30 * time.Second

// Messenger is the per-user runtime.
type Messenger struct {
	cfg Config

	logger *slog.Logger

	sessionsMu sync.Mutex
	sessions   map[sessionKey]*Session

	incomingMu sync.RWMutex
	incoming   func(*Session)

	peerOnlineMu sync.RWMutex
	peerOnline   func(identity.Hash)

	closeOnce sync.Once
}

type sessionKey struct {
	peer identity.Hash
	sid  network.SessionID
}

// Open constructs a Messenger over the given Network and Storage.
// It does not open IO — Run starts the loops; Close tears them down
// without affecting the Network or Storage owned by the caller.
func Open(cfg Config) *Messenger {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Messenger{
		cfg:      cfg,
		logger:   cfg.Logger,
		sessions: make(map[sessionKey]*Session),
	}
}

// Identity returns the local identity (delegated from Network).
func (m *Messenger) Identity() *identity.Identity { return m.cfg.Network.Identity() }

// LocalAddress returns the externally-reachable transport address
// (delegated from Network).
func (m *Messenger) LocalAddress() string { return m.cfg.Network.LocalAddress() }

// Storage exposes the SQLite store for the UI layer.
func (m *Messenger) Storage() *storage.Store { return m.cfg.Storage }

// Logger returns the configured slog.Logger.
func (m *Messenger) Logger() *slog.Logger { return m.logger }

// SetIncomingHandler registers a callback for inbound signaling sessions
// initiated by remote peers. Replaces any prior handler.
func (m *Messenger) SetIncomingHandler(fn func(*Session)) {
	m.incomingMu.Lock()
	defer m.incomingMu.Unlock()
	m.incoming = fn
}

// SetPeerOnlineHandler registers a callback fired by the outbox flush
// pump each time a recipient with pending items becomes resolvable in
// presence. The UI uses this to wake up its delivery flow.
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

// Run starts the background loops. errgroup propagates the first error;
// clean ctx-cancel returns nil. Caller is expected to run Network.Run
// in a separate goroutine — messenger does not start the network for
// the caller.
func (m *Messenger) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error { return m.incomePump(gctx) })

	if interval := m.outboxInterval(); interval > 0 {
		g.Go(func() error {
			m.outboxPump(gctx, interval)

			return nil
		})
	}

	g.Go(func() error {
		m.historyRetentionPump(gctx)

		return nil
	})

	g.Go(func() error {
		m.outboxRetentionPump(gctx)

		return nil
	})

	return g.Wait()
}

// outboxRetentionPump drops outbox items older than OutboxRetentionDays
// every hour. Tolerant — any error logs and the next tick retries.
func (m *Messenger) outboxRetentionPump(ctx context.Context) {
	tick := time.NewTicker(HistoryRetentionTickInterval)
	defer tick.Stop()

	prune := func() {
		cutoff := time.Now().Add(-OutboxRetentionDays * 24 * time.Hour).Unix()
		n, err := m.cfg.Storage.PruneOutboxOlderThan(ctx, cutoff)
		if err != nil {
			m.logger.Warn("messenger: outbox prune", "err", err)
			return
		}
		if n > 0 {
			m.logger.Info("messenger: outbox prune", "rows", n)
		}
	}

	prune()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			prune()
		}
	}
}

// HistoryRetentionTickInterval is how often the runtime re-checks the
// "delete history older than N days" setting and prunes if needed.
// Hourly is plenty — the setting is coarse-grained.
const HistoryRetentionTickInterval = time.Hour

// OutboxRetentionDays is the maximum age of a pending outbox item.
// Longer than this and the recipient is presumed lost; the message is
// dropped to keep the table bounded. 7 days mirrors the audit-log
// retention.
const OutboxRetentionDays = 7

// SettingKeyHistoryRetainDays is the app_settings row holding the chat
// history retention window in days; 0 (or missing) disables auto-prune.
const SettingKeyHistoryRetainDays = "history.retain_days"

// historyRetentionPump deletes message rows older than the configured
// retention window. Cheap (one DELETE) and tolerant — any error is
// logged and the next tick retries.
func (m *Messenger) historyRetentionPump(ctx context.Context) {
	tick := time.NewTicker(HistoryRetentionTickInterval)
	defer tick.Stop()

	prune := func() {
		v, ok, err := m.cfg.Storage.GetSetting(ctx, SettingKeyHistoryRetainDays)
		if err != nil || !ok || v == "" {
			return
		}
		days, err := strconv.Atoi(v)
		if err != nil || days <= 0 {
			return
		}
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixNano()
		n, err := m.cfg.Storage.PruneMessagesOlderThan(ctx, cutoff)
		if err != nil {
			m.logger.Warn("messenger: history prune", "err", err)
			return
		}
		if n > 0 {
			m.logger.Info("messenger: history prune", "rows", n, "older_than_days", days)
		}
	}

	prune()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			prune()
		}
	}
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

// incomePump is the single consumer of network.Income(). It demuxes by
// (peer, sessionID) into per-Session inboxes; first event with a new
// sessionID = new incoming session, fires the SetIncomingHandler.
func (m *Messenger) incomePump(ctx context.Context) error {
	income := m.cfg.Network.Income()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-income:
			if !ok {
				return nil // network shut down
			}
			m.handleIncome(ev)
		}
	}
}

func (m *Messenger) handleIncome(ev *network.Income) {
	defer ev.Release()

	sess, isNew := m.lookupOrInstallSession(ev.Peer, ev.SessionID, ev.PeerPublic)

	if ev.Final {
		sess.shutdown()
		return
	}

	if len(ev.Payload) > 0 {
		// handleIncomingPayload unmarshals (which copies bytes into
		// SignedSDP) before pushing into inbox — safe to Release the
		// pool buffer immediately on return.
		sess.handleIncomingPayload(ev.Payload)
	}

	if isNew {
		m.incomingMu.RLock()
		handler := m.incoming
		m.incomingMu.RUnlock()
		if handler != nil {
			go handler(sess)
		} else {
			// No UI consumer registered (very early startup or after
			// shutdown) — best-effort tear-down. Close error is logged
			// because a failure here implies the network session was
			// already in a degraded state we want to know about.
			m.logger.Warn("messenger: incoming session with no handler; closing", "peer", ev.Peer)
			if err := sess.Close(); err != nil {
				m.logger.Debug("messenger: close orphan incoming session", "peer", ev.Peer, "err", err)
			}
		}
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
//
// Storage errors are surfaced at Warn (they indicate a real fault — the
// outbox cannot do its job). Per-peer Lookup misses are expected
// (recipient simply offline) and logged at Debug only.
func (m *Messenger) FlushOutboxOnce(ctx context.Context) {
	contacts, err := m.cfg.Storage.ListContacts(ctx)
	if err != nil {
		m.logger.Warn("messenger: outbox flush: list contacts", "err", err)

		return
	}
	for _, c := range contacts {
		select {
		case <-ctx.Done():
			return
		default:
		}
		items, err := m.cfg.Storage.PendingForPeer(ctx, c.Hash)
		if err != nil {
			m.logger.Warn("messenger: outbox flush: pending for peer", "peer", c.Hash, "err", err)

			continue
		}
		if len(items) == 0 {
			continue
		}

		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, lookupErr := m.cfg.Network.Lookup(rctx, c.Hash)
		cancel()
		if lookupErr != nil {
			// ErrPeerNotFound is the common case — recipient offline.
			// Anything else (ctx-cancel propagation, malformed record,
			// transport teardown) is worth surfacing at Debug.
			if !errors.Is(lookupErr, network.ErrPeerNotFound) && !errors.Is(lookupErr, context.Canceled) && !errors.Is(lookupErr, context.DeadlineExceeded) {
				m.logger.Debug("messenger: outbox flush: presence lookup", "peer", c.Hash, "err", lookupErr)
			}

			continue
		}
		m.firePeerOnline(c.Hash)
	}
}

// Close tears down active sessions. Does NOT close Network or Storage
// — those are owned by the caller.
func (m *Messenger) Close() {
	m.closeOnce.Do(func() {
		// Snapshot before shutting down sessions — sess.shutdown calls
		// removeSession which re-acquires sessionsMu (would self-deadlock).
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
	})
}

// Connect opens a signaling session to peer (initiator side). Returns
// a Session that the UI layer drives — sending offers and receiving
// answers + ICE through it.
func (m *Messenger) Connect(ctx context.Context, peer identity.Hash) (*Session, error) {
	nsess, err := m.cfg.Network.Connect(ctx, peer)
	if err != nil {
		return nil, err
	}

	sess, _ := m.lookupOrInstallSessionWith(nsess.Peer, nsess.SessionID, nsess.PeerPublic, nsess)

	return sess, nil
}

// lookupOrInstallSession returns an existing session if one is already
// installed for the (peer, sid), otherwise creates a fresh one. The
// underlying network.Session is constructed with the same (peer, sid,
// pub) — Send/Close on either end translate to the same network call.
func (m *Messenger) lookupOrInstallSession(peer identity.Hash, sid network.SessionID, peerPub identity.PublicIdentity) (*Session, bool) {
	nsess := m.cfg.Network.SessionFor(peer, sid, peerPub)

	return m.lookupOrInstallSessionWith(peer, sid, peerPub, nsess)
}

func (m *Messenger) lookupOrInstallSessionWith(peer identity.Hash, sid network.SessionID, peerPub identity.PublicIdentity, nsess network.Session) (*Session, bool) {
	key := sessionKey{peer: peer, sid: sid}

	m.sessionsMu.Lock()
	if existing, ok := m.sessions[key]; ok {
		m.sessionsMu.Unlock()

		return existing, false
	}

	s := &Session{
		Peer:       peer,
		SessionID:  sessionIDString(sid),
		underlying: nsess,
		messenger:  m,
		peerPub:    peerPub,
		inbox:      make(chan SignalEvent, 32),
		closed:     make(chan struct{}),
	}
	m.sessions[key] = s
	m.sessionsMu.Unlock()

	return s, true
}

func (m *Messenger) removeSession(peer identity.Hash, sid string) {
	m.sessionsMu.Lock()
	for k := range m.sessions {
		if k.peer == peer && sessionIDString(k.sid) == sid {
			delete(m.sessions, k)
			break
		}
	}
	m.sessionsMu.Unlock()
}

func sessionIDString(sid network.SessionID) string {
	return hex.EncodeToString(sid[:])
}

// ErrPeerUnknown is returned when the runtime has no public-identity
// material for the peer — neither cached locally nor resolvable via
// presence — so a session cannot be authenticated.
var ErrPeerUnknown = errors.New("messenger: peer unknown")
