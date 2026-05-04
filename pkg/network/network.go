// Package network is a reusable runtime that joins a DHT, runs a
// signaling-relay (passive: shares the same UDP socket as the DHT so
// peers can route through it while looking up other peers), optionally
// serves embedded STUN/TURN volunteers, and exposes an opaque session
// API for higher layers (e.g. internal/messenger). It encapsulates the
// composition of pkg/transport, pkg/dht, pkg/signaling, pkg/presence,
// pkg/stun and pkg/turn so consumers depend on this single package.
package network

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/udisondev/udisend/pkg/bootstrap"
	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/stun"
	"github.com/udisondev/udisend/pkg/transport"
	"github.com/udisondev/udisend/pkg/turn"
)

// Mode selects the role this node plays in the DHT.
type Mode int

const (
	// ModeClient is the default: a participant that publishes its own
	// presence (CapPublicIP if it has a routable IP) and uses the
	// network to reach peers.
	ModeClient Mode = iota
	// ModeRelay advertises CapCanRelay | CapCanBootstrap (and
	// STUN/TURN if PublicIP is set). Operators run dedicated relay
	// nodes in this mode.
	ModeRelay
)

// SeenPeerStore is an optional cache of previously-reachable bootstrap
// addresses. When Config.Bootstrap is empty, Open consults the store
// before falling back to bootstrap.Defaults. Implementations live above
// pkg/network (e.g. internal/storage); this interface is the only seam.
type SeenPeerStore interface {
	SeenPeersDiverse(ctx context.Context, n int) ([]string, error)
	RecordSeenPeer(ctx context.Context, addr string) error
	ForgetSeenPeer(ctx context.Context, addr string) error
}

// Config configures Open.
type Config struct {
	Identity      *identity.Identity
	Mode          Mode
	Listen        string   // UDP listen address; ":0" picks ephemeral
	Bootstrap     []string // peer addresses to seed the routing table
	PublicIP      string   // if non-empty, STUN/TURN are advertised on this IP
	STUNAddr      string   // optional, defaults to ":3478" if PublicIP set (ModeRelay)
	TURNAddr      string   // optional, defaults to ":3479" if PublicIP set (ModeRelay)
	TURNSecret    string   // shared secret for TURN long-term-credentials
	PresenceTTL   time.Duration
	Logger        *slog.Logger
	SeenPeerStore SeenPeerStore // optional bootstrap cache
	// IncomeBuffer caps the size of the Income channel returned by
	// Income(). Zero uses DefaultIncomeBuffer. The pump blocks on full
	// (no drops) — backpressure for signaling.
	IncomeBuffer int
}

// DefaultIncomeBuffer is the size of the Income channel when Config
// leaves IncomeBuffer unset.
const DefaultIncomeBuffer = 256

// Errors returned by the package.
var (
	ErrUnknownSession = errors.New("network: unknown session")
	// ErrPeerNotFound is returned by Lookup when no presence record
	// exists for the peer (re-export of presence.ErrNotFound; the
	// underlying type identity is preserved so errors.Is works).
	ErrPeerNotFound = presence.ErrNotFound
)

// Node bundles every long-lived process inside a network node.
type Node struct {
	cfg Config

	transport *transport.UDPTransport
	dht       *dht.Node
	publisher *presence.Publisher
	resolver  *presence.Resolver
	signaling *signaling.Service
	stunSrv   *stun.Server
	turnSrv   *turn.Server

	sessMu   sync.RWMutex
	sessions map[sessionKey]*signaling.Channel

	income chan *Income

	pumpWg sync.WaitGroup // tracks per-session pump goroutines

	runMu  sync.RWMutex
	runCtx context.Context

	closeOnce sync.Once
}

type sessionKey struct {
	peer identity.Hash
	sid  SessionID
}

// Open spins up a fresh node. Call Run to start the loops.
func Open(ctx context.Context, cfg Config) (*Node, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Listen == "" {
		cfg.Listen = ":0"
	}
	if cfg.PresenceTTL == 0 {
		cfg.PresenceTTL = presence.DefaultRecordTTL
	}
	if cfg.IncomeBuffer <= 0 {
		cfg.IncomeBuffer = DefaultIncomeBuffer
	}

	// design.md §7-8: bootstrap source priority — explicit cfg.Bootstrap,
	// then the seen-peers cache (subnet-diverse to dilute Sybil clusters),
	// then the curated community list + DNS seeds.
	if len(cfg.Bootstrap) == 0 && cfg.SeenPeerStore != nil {
		cached, err := cfg.SeenPeerStore.SeenPeersDiverse(ctx, 50)
		switch {
		case err != nil:
			// Cache lookup is best-effort — proceed to community defaults.
			// We log so a recurring DB error is visible in operator logs.
			cfg.Logger.Warn("network: seen-peers cache lookup failed; falling back to defaults", "err", err)
		case len(cached) > 0:
			cfg.Bootstrap = cached
			cfg.Logger.Info("network: bootstrap from cache", "n", len(cached))
		}
	}
	if len(cfg.Bootstrap) == 0 {
		if defaults := bootstrap.Defaults(ctx, bootstrap.Config{}); len(defaults) > 0 {
			cfg.Bootstrap = defaults
			cfg.Logger.Info("network: bootstrap from community defaults", "n", len(defaults))
		}
	}

	tr, err := transport.ListenUDP(cfg.Listen)
	if err != nil {
		return nil, err
	}

	n := &Node{
		cfg:       cfg,
		transport: tr,
		sessions:  make(map[sessionKey]*signaling.Channel),
		income:    make(chan *Income, cfg.IncomeBuffer),
	}

	n.signaling = signaling.NewService(signaling.Config{
		Identity:  cfg.Identity,
		Transport: tr,
		Resolver:  &resolverDelegate{n: n},
		Logger:    cfg.Logger,
	})

	store := presence.NewRateLimitedStore(dht.NewMemoryStore(nil))
	n.dht = dht.NewNode(cfg.Identity, tr, store, dht.Config{
		Logger:       cfg.Logger,
		ExtraHandler: n.signaling.HandlePacket,
	})
	n.signaling.SetRouter(newDHTRouter(n.dht))
	n.signaling.SetHandler(n.onIncomingChannel)

	n.resolver = presence.NewResolver(nodeAdapter{n.dht}, cfg.PresenceTTL)

	caps := capsFor(cfg.Mode, cfg.PublicIP != "")
	pubAddr := tr.LocalAddr().String()
	if cfg.PublicIP != "" {
		// Operators routinely bind to 0.0.0.0; the published record
		// must carry a routable address — substitute the public IP
		// keeping the bound port. transport.LocalAddr is always
		// host:port for UDP, but if the format ever changes we log
		// rather than silently advertise an unroutable record.
		_, port, err := net.SplitHostPort(pubAddr)
		if err != nil {
			// Roll back the transport we just opened. Close() error
			// here is irrelevant — we are returning the parse failure
			// to the caller, which is the actionable signal.
			_ = tr.Close()

			return nil, fmt.Errorf("network: parse local addr %q: %w", pubAddr, err)
		}
		pubAddr = net.JoinHostPort(cfg.PublicIP, port)
	}
	n.publisher = presence.NewPublisher(presence.PublisherConfig{
		Identity:     cfg.Identity,
		Address:      pubAddr,
		Capabilities: caps,
		TTL:          cfg.PresenceTTL,
		DHT:          nodeAdapter{n.dht},
		Logger:       cfg.Logger,
	})

	if cfg.PublicIP != "" && cfg.Mode == ModeRelay {
		stunAddr := cfg.STUNAddr
		if stunAddr == "" {
			stunAddr = ":3478"
		}
		ssrv, err := stun.Listen(stunAddr)
		if err != nil {
			cfg.Logger.Warn("network: STUN listen failed; skipping", "err", err)
		} else {
			n.stunSrv = ssrv
		}

		if cfg.TURNSecret != "" {
			turnAddr := cfg.TURNAddr
			if turnAddr == "" {
				turnAddr = ":3479"
			}
			tsrv, err := turn.NewServer(turn.Config{
				PublicIP:     cfg.PublicIP,
				ListenAddr:   turnAddr,
				SharedSecret: cfg.TURNSecret,
			})
			if err != nil {
				cfg.Logger.Warn("network: TURN listen failed; skipping", "err", err)
			} else {
				n.turnSrv = tsrv
			}
		}
	}

	return n, nil
}

func capsFor(mode Mode, hasPublicIP bool) presence.Capability {
	switch mode {
	case ModeRelay:
		caps := presence.CapCanRelay | presence.CapCanBootstrap
		if hasPublicIP {
			caps |= presence.CapPublicIP | presence.CapCanSTUN | presence.CapCanTURN
		}

		return caps
	default:
		// ModeClient
		if hasPublicIP {
			return presence.CapPublicIP
		}

		return 0
	}
}

// Run starts every loop and blocks until ctx is cancelled or any loop
// returns an error. errgroup propagates the first error; clean cancel
// returns nil.
//
// Order matters:
//
//  1. DHT recv loop must be running first so PING/NODES responses
//     arrive during bootstrap.
//  2. Bootstrap is awaited synchronously (with a per-peer timeout).
//     Without this, publisher's first PutValue races bootstrap: an
//     empty routing table at PutValue time means the record is stored
//     ONLY locally, and remote peers cannot find us until the next
//     refresh (TTL/2 ≈ 45 s). That manifests as "record not found"
//     when a peer tries to add us as a contact right after we start.
//  3. Publisher starts last so its first publish reaches the live
//     bootstrap peer.
func (n *Node) Run(ctx context.Context) error {
	n.runMu.Lock()
	n.runCtx = ctx
	n.runMu.Unlock()

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		n.dht.Run(gctx)

		return nil
	})

	// Best-effort bootstrap: warn-and-continue per peer, never propagate
	// — a dead bootstrap entry must not collapse the whole node.
	if len(n.cfg.Bootstrap) > 0 {
		n.bootstrapAll(gctx)
	}

	g.Go(func() error {
		n.publisher.Run(gctx)

		return nil
	})
	// STUN/TURN are auxiliary roles for relay nodes — a failure must not
	// collapse the DHT/signaling loops, so we log and continue rather
	// than letting errgroup propagate the error. Run() returning early is
	// tantamount to that role being offline; the rest of the node stays up.
	if n.stunSrv != nil {
		g.Go(func() error {
			if err := n.stunSrv.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
				n.cfg.Logger.Warn("network: STUN server exited with error", "err", err)
			}

			return nil
		})
	}
	if n.turnSrv != nil {
		g.Go(func() error {
			if err := n.turnSrv.Run(gctx); err != nil && !errors.Is(err, context.Canceled) {
				n.cfg.Logger.Warn("network: TURN server exited with error", "err", err)
			}

			return nil
		})
	}

	err := g.Wait()
	closeErr := n.closeServers()
	n.pumpWg.Wait()
	close(n.income)

	return errors.Join(err, closeErr)
}

func (n *Node) bootstrapAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, addr := range n.cfg.Bootstrap {
		wg.Go(func() {
			peer, err := n.transport.Dial(addr)
			if err != nil {
				n.cfg.Logger.Warn("network: bootstrap parse", "addr", addr, "err", err)
				return
			}

			bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			if err := n.dht.Bootstrap(bctx, peer); err != nil {
				n.cfg.Logger.Warn("network: bootstrap", "addr", addr, "err", err)
				if n.cfg.SeenPeerStore != nil {
					if rmErr := n.cfg.SeenPeerStore.ForgetSeenPeer(ctx, addr); rmErr != nil {
						n.cfg.Logger.Debug("network: forget seen peer", "addr", addr, "err", rmErr)
					}
				}

				return
			}

			n.cfg.Logger.Info("network: bootstrap ok", "addr", addr)
			if n.cfg.SeenPeerStore != nil {
				if recErr := n.cfg.SeenPeerStore.RecordSeenPeer(ctx, addr); recErr != nil {
					n.cfg.Logger.Debug("network: record seen peer", "addr", addr, "err", recErr)
				}
			}
		})
	}
	wg.Wait()
}

// Close releases transport/STUN/TURN/signaling sockets. Run also calls
// closeServers internally on exit, so explicit Close from main is only
// needed if you abandon Run. Errors from each subordinate Close are
// aggregated via errors.Join so callers see the full failure surface,
// not just whichever happened first.
func (n *Node) Close() error {
	return n.closeServers()
}

func (n *Node) closeServers() error {
	var joined error
	n.closeOnce.Do(func() {
		// signaling.Service.Close is void by contract — it tears down
		// channels best-effort and returns nothing to aggregate.
		n.signaling.Close()
		if n.stunSrv != nil {
			if err := n.stunSrv.Close(); err != nil {
				joined = errors.Join(joined, fmt.Errorf("network: stun close: %w", err))
			}
		}
		if n.turnSrv != nil {
			if err := n.turnSrv.Close(); err != nil {
				joined = errors.Join(joined, fmt.Errorf("network: turn close: %w", err))
			}
		}
		if err := n.transport.Close(); err != nil {
			joined = errors.Join(joined, fmt.Errorf("network: transport close: %w", err))
		}
	})

	return joined
}

// LocalAddress returns the bound UDP address — useful in tests / logs.
func (n *Node) LocalAddress() string { return n.transport.LocalAddr().String() }

// Identity returns the node's identity. Higher layers need it to sign
// application-level envelopes (the network signs only its own presence
// records).
func (n *Node) Identity() *identity.Identity { return n.cfg.Identity }

// Income returns the channel of low-level inbound events. ONE consumer
// is supported (the messenger). The channel closes after Run returns.
func (n *Node) Income() <-chan *Income { return n.income }

// SessionFor builds a value-typed Session handle for an already-
// registered session. Higher layers consuming Income events use this
// to construct their per-session wrappers without re-running Connect.
// No IO; caller must ensure the session was previously registered.
func (n *Node) SessionFor(peer identity.Hash, sid SessionID, peerPub identity.PublicIdentity) Session {
	return Session{
		Peer:       peer,
		SessionID:  sid,
		PeerPublic: peerPub,
		node:       n,
	}
}

// Connect initiates a signaling session to peer. Returns a value-typed
// Session handle; subsequent Send/Close go via the Node by (peer, sid).
func (n *Node) Connect(ctx context.Context, peer identity.Hash) (Session, error) {
	rec, err := n.resolver.Lookup(ctx, peer)
	if err != nil {
		return Session{}, err
	}

	ch, err := n.signaling.Connect(ctx, peer)
	if err != nil {
		return Session{}, err
	}
	n.registerChannel(ch, rec.Public)

	return Session{
		Peer:       peer,
		SessionID:  ch.SessionID(),
		PeerPublic: rec.Public,
		node:       n,
	}, nil
}

// Send delivers payload over an existing session. Returns
// ErrUnknownSession if (peer, sid) is not registered.
//
// payload is owned by the caller; network does not retain it after
// return (signaling.Channel.Send copies/encrypts internally).
func (n *Node) Send(ctx context.Context, peer identity.Hash, sid SessionID, payload []byte) error {
	n.sessMu.RLock()
	ch, ok := n.sessions[sessionKey{peer: peer, sid: sid}]
	n.sessMu.RUnlock()
	if !ok {
		return ErrUnknownSession
	}

	return ch.Send(ctx, payload)
}

// CloseSession sends a graceful BYE to the peer and tears down the
// session. Returns ErrUnknownSession if (peer, sid) is not registered.
func (n *Node) CloseSession(peer identity.Hash, sid SessionID) error {
	n.sessMu.RLock()
	ch, ok := n.sessions[sessionKey{peer: peer, sid: sid}]
	n.sessMu.RUnlock()
	if !ok {
		return ErrUnknownSession
	}

	return ch.Close()
}

// onIncomingChannel is the signaling.Service handler. It resolves the
// peer's public identity, starts the income pump, and emits a marker
// Income (Payload nil, Final false) so consumers can react to a new
// session before the peer's first DATA frame arrives. Runs in its own
// goroutine (signaling spawns it via `go (*h)(...)`).
func (n *Node) onIncomingChannel(peer identity.Hash, ch *signaling.Channel) {
	rctx := n.runContext()

	lookupCtx, cancel := context.WithTimeout(rctx, 4*time.Second)
	rec, err := n.resolver.Lookup(lookupCtx, peer)
	cancel()
	if err != nil {
		n.cfg.Logger.Warn("network: incoming peer pubkey lookup failed", "peer", peer, "err", err)
		if cerr := ch.Close(); cerr != nil {
			n.cfg.Logger.Debug("network: close orphan incoming channel", "peer", peer, "err", cerr)
		}

		return
	}

	n.registerChannel(ch, rec.Public)
	n.emitOpened(peer, ch.SessionID(), rec.Public)
}

// emitOpened emits a marker Income that lets the consumer install its
// per-session state before any DATA arrives. Only called on the
// responder side — initiators already hold the Session handle from
// Connect.
func (n *Node) emitOpened(peer identity.Hash, sid SessionID, peerPub identity.PublicIdentity) {
	inc := newIncome()
	inc.Peer = peer
	inc.SessionID = sid
	inc.PeerPublic = peerPub

	select {
	case n.income <- inc:
	case <-n.runContext().Done():
		inc.Release()
	}
}

// registerChannel installs the channel in the sessions map and starts
// its income pump. Any prior channel under the same key is shut down
// first (snapshotted outside the lock to avoid AB/BA deadlock with
// Channel.Close which re-acquires sessMu via unregisterChannel).
func (n *Node) registerChannel(ch *signaling.Channel, peerPub identity.PublicIdentity) {
	peer := ch.Peer()
	sid := ch.SessionID()
	key := sessionKey{peer: peer, sid: sid}

	n.sessMu.Lock()
	old := n.sessions[key]
	n.sessions[key] = ch
	n.sessMu.Unlock()

	if old != nil && old != ch {
		if cerr := old.Close(); cerr != nil {
			n.cfg.Logger.Debug("network: close superseded session channel", "peer", peer, "err", cerr)
		}
	}

	n.pumpWg.Add(1)
	go n.pumpSession(ch, peerPub, peer, sid)
}

func (n *Node) unregisterChannel(peer identity.Hash, sid SessionID) {
	n.sessMu.Lock()
	delete(n.sessions, sessionKey{peer: peer, sid: sid})
	n.sessMu.Unlock()
}

// pumpSession reads frames from ch and emits them to n.income tagged
// with peer/sessionID/peerPub. On error or close, emits one final
// Income{Final: true} and exits.
func (n *Node) pumpSession(ch *signaling.Channel, peerPub identity.PublicIdentity, peer identity.Hash, sid SessionID) {
	defer n.pumpWg.Done()
	defer n.unregisterChannel(peer, sid)

	rctx := n.runContext()
	for {
		payload, err := ch.Recv(rctx)
		if err != nil {
			// Recv returns on graceful BYE, ctx-cancel, or transport
			// teardown. Only the last is interesting for diagnostics;
			// the others are normal lifecycle. Log at Debug — caller
			// is told via emitFinal regardless.
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				n.cfg.Logger.Debug("network: session recv ended", "peer", peer, "err", err)
			}
			n.emitFinal(peer, sid, peerPub)

			return
		}

		inc := newIncome()
		inc.Peer = peer
		inc.SessionID = sid
		inc.PeerPublic = peerPub
		inc.Payload = payload

		select {
		case n.income <- inc:
		case <-rctx.Done():
			inc.Release()
			n.emitFinal(peer, sid, peerPub)
			return
		}
	}
}

func (n *Node) emitFinal(peer identity.Hash, sid SessionID, peerPub identity.PublicIdentity) {
	inc := newIncome()
	inc.Peer = peer
	inc.SessionID = sid
	inc.PeerPublic = peerPub
	inc.Final = true

	rctx := n.runContext()
	select {
	case n.income <- inc:
	case <-rctx.Done():
		inc.Release()
	}
}

// runContext returns the ctx passed to Run, or a closed Background ctx
// if Run hasn't started yet (defensive — handlers should not fire
// before Run, but signaling can technically deliver a packet between
// Open and Run if the transport already saw one).
func (n *Node) runContext() context.Context {
	n.runMu.RLock()
	ctx := n.runCtx
	n.runMu.RUnlock()
	if ctx == nil {
		return context.Background()
	}

	return ctx
}

// resolverDelegate routes signaling's AddressResolver lookups through
// our presence.Resolver. Defined here (not as a closure) so the
// signaling.Service field is a stable interface reference.
type resolverDelegate struct{ n *Node }

func (r *resolverDelegate) Lookup(ctx context.Context, peer identity.Hash) (*presence.Record, error) {
	return r.n.resolver.Lookup(ctx, peer)
}
