// Package network is a reusable runtime that joins a DHT, runs a
// signaling-relay (passive: shares the same UDP socket as the DHT so
// peers can route through it while looking up other peers), optionally
// serves embedded STUN/TURN volunteers and a WebRTC mesh overlay, and
// exposes an opaque session API for higher layers (e.g.
// internal/messenger). It encapsulates the composition of
// pkg/transport, pkg/dht, pkg/signaling, pkg/presence, pkg/stun,
// pkg/turn, pkg/webrtc.
//
// Reusability boundary: this package is the udisend-specific assembly
// of the lower-level pkg/* components. External consumers wanting only
// some of the stack — e.g. "kademlia + signaling, no mesh" or "just
// the WebRTC mesh manager" — should compose pkg/dht, pkg/signaling,
// pkg/transport, pkg/webrtc directly; they are designed to stand on
// their own. Importing pkg/network pulls in pion/webrtc transitively
// even when MeshEnabled is false; full mesh-opt-out at the import-graph
// level is deferred follow-up work.
//
// All public boundary types accept identity.PeerID (Suite-agnostic);
// internal storage stays in identity.Hash since this package owns the
// peer-id values it tracks.
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
	"github.com/udisondev/udisend/pkg/webrtc"
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

// BootstrapOverrideStore is an optional source of user-curated bootstrap
// addresses managed at runtime (e.g. via the webui Settings → Bootstrap
// panel). When Config.Bootstrap is empty, Open consults this store
// BEFORE the seen-peers cache so user picks always win on a fresh start.
// MarkBootstrapStatus is invoked after every dial attempt; callers free
// to silently ignore unknown addresses (we feed it every bootstrap
// outcome regardless of source).
type BootstrapOverrideStore interface {
	EnabledBootstrapOverrides(ctx context.Context) ([]string, error)
	MarkBootstrapStatus(ctx context.Context, addr, status string, when time.Time) error
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
	// BootstrapOverrideStore is an optional source of user-managed
	// bootstrap addresses. Consulted before SeenPeerStore.
	BootstrapOverrideStore BootstrapOverrideStore
	// IncomeBuffer caps the size of the Income channel returned by
	// Income(). Zero uses DefaultIncomeBuffer. The pump blocks on full
	// (no drops) — backpressure for signaling.
	IncomeBuffer int
	// Now, when non-nil, replaces time.Now in time-sensitive paths
	// (currently bootstrapOne's success/failure timestamp). Tests pin
	// it to a fixture; production callers leave it nil for time.Now.
	Now func() time.Time
	// Transport, when non-nil, is used as the underlying transport
	// instead of opening a UDP socket via Listen. Tests inject a
	// transport.MemoryHub-backed transport so the entire stack runs
	// in-process. Production callers leave it nil.
	Transport transport.Transport

	// MeshEnabled, when true, spins up the inter-node WebRTC mesh:
	// persistent DataChannels between nodes that survive a coordinated
	// outage of public-IP signaling-relay nodes. The mesh runs in
	// parallel to the UDP DHT/signaling stack — DHT RPC and signaling
	// envelopes still ride UDP — so leaving this off is the safe
	// default when callers do not need the overlay.
	MeshEnabled bool

	// MaxMeshLinks caps the live PeerSession count when mesh is
	// enabled. 0 falls back to webrtc.DefaultMaxLinks (8).
	MaxMeshLinks int

	// SignalingHandshakeTimeout overrides signaling.DefaultHandshakeTimeout
	// (6s) for the Noise XK 3-message handshake. Production callers leave
	// it zero. Tests under -race + heavy parallelism set it to 20-30s
	// because race-detector overhead can starve pion / noise enough
	// to miss the production-tuned window — without this knob the
	// mesh-e2e suite flakes when run alongside other heavy packages.
	SignalingHandshakeTimeout time.Duration
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

	transport transport.Transport
	dht       *dht.Node
	publisher *presence.Publisher
	resolver  *presence.Resolver
	signaling *signaling.Service
	stunSrv   *stun.Server
	turnSrv   *turn.Server

	// Mesh stack — nil unless cfg.MeshEnabled.
	mesh         *MeshSignaler
	rtcTransport *transport.WebRTCTransport
	peerManager  *webrtc.PeerManager

	sessMu   sync.RWMutex
	sessions map[sessionKey]*signaling.Channel

	income *incomeChannel

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
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	if err := resolveBootstrap(ctx, &cfg); err != nil {
		return nil, err
	}

	tr, err := openTransport(cfg)
	if err != nil {
		return nil, err
	}

	n := &Node{
		cfg:       cfg,
		transport: tr,
		sessions:  make(map[sessionKey]*signaling.Channel),
		income:    newIncomeChannel(cfg.IncomeBuffer),
	}

	n.signaling = signaling.NewService(signaling.Config{
		Identity:         cfg.Identity,
		Transport:        tr,
		Resolver:         &resolverDelegate{n: n},
		Logger:           cfg.Logger,
		HandshakeTimeout: cfg.SignalingHandshakeTimeout,
	})

	store := presence.NewRateLimitedStore(dht.NewMemoryStore(nil))
	n.dht = dht.NewNode(cfg.Identity, tr, store, dht.Config{
		Logger:       cfg.Logger,
		ExtraHandler: n.signaling.HandlePacket,
	})
	n.signaling.SetRouter(newDHTRouter(n.dht))
	n.signaling.SetHandler(n.onIncomingChannel)

	n.resolver = presence.NewResolver(n.dht, cfg.PresenceTTL)

	caps := capsFor(cfg.Mode, cfg.PublicIP != "", cfg.MeshEnabled)
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
		DHT:          n.dht,
		Logger:       cfg.Logger,
	})

	if cfg.MeshEnabled {
		if err := n.buildMesh(); err != nil {
			_ = tr.Close()

			return nil, fmt.Errorf("network: build mesh: %w", err)
		}
	}

	if cfg.PublicIP != "" && cfg.Mode == ModeRelay {
		n.startSTUN(cfg)
		n.startTURN(cfg)
	}

	return n, nil
}

// resolveBootstrap fills cfg.Bootstrap from a priority chain: explicit
// Bootstrap → user overrides → seen-peers cache → curated community
// list + DNS seeds. The first non-empty source wins; we do not
// currently merge across tiers.
func resolveBootstrap(ctx context.Context, cfg *Config) error {
	if len(cfg.Bootstrap) > 0 {
		return nil
	}

	if cfg.BootstrapOverrideStore != nil {
		picks, err := cfg.BootstrapOverrideStore.EnabledBootstrapOverrides(ctx)
		if err != nil {
			cfg.Logger.Warn("network: bootstrap-overrides lookup failed; falling through", "err", err)
		} else if len(picks) > 0 {
			cfg.Bootstrap = picks
			cfg.Logger.Info("network: bootstrap from user overrides", "n", len(picks))

			return nil
		}
	}

	if cfg.SeenPeerStore != nil {
		cached, err := cfg.SeenPeerStore.SeenPeersDiverse(ctx, 50)
		if err != nil {
			// Cache lookup is best-effort — proceed to community defaults.
			cfg.Logger.Warn("network: seen-peers cache lookup failed; falling back to defaults", "err", err)
		} else if len(cached) > 0 {
			cfg.Bootstrap = cached
			cfg.Logger.Info("network: bootstrap from cache", "n", len(cached))

			return nil
		}
	}

	if defaults := bootstrap.Defaults(ctx, bootstrap.Config{}); len(defaults) > 0 {
		cfg.Bootstrap = defaults
		cfg.Logger.Info("network: bootstrap from community defaults", "n", len(defaults))
	}

	return nil
}

func openTransport(cfg Config) (transport.Transport, error) {
	if cfg.Transport != nil {
		return cfg.Transport, nil
	}

	return transport.ListenUDP(cfg.Listen)
}

func (n *Node) startSTUN(cfg Config) {
	addr := cfg.STUNAddr
	if addr == "" {
		addr = ":3478"
	}
	srv, err := stun.Listen(addr)
	if err != nil {
		cfg.Logger.Warn("network: STUN listen failed; skipping", "err", err)

		return
	}
	n.stunSrv = srv
}

func (n *Node) startTURN(cfg Config) {
	if cfg.TURNSecret == "" {
		return
	}
	addr := cfg.TURNAddr
	if addr == "" {
		addr = ":3479"
	}
	srv, err := turn.NewServer(turn.Config{
		PublicIP:     cfg.PublicIP,
		ListenAddr:   addr,
		SharedSecret: cfg.TURNSecret,
	})
	if err != nil {
		cfg.Logger.Warn("network: TURN listen failed; skipping", "err", err)

		return
	}
	n.turnSrv = srv
}

// buildMesh wires the inter-node mesh stack. It runs as a best-effort
// layer alongside the UDP DHT/signaling — if any piece fails to
// construct, the whole node refuses to start so the operator notices
// rather than silently shipping a half-mesh.
//
// Order: MeshSignaler bridges signaling.Service → WebRTCTransport
// → PeerManager (driven by a DHT-backed selector). The composite
// transport is NOT installed: DHT and signaling continue to use the
// UDP transport directly because their peer addresses are UDPAddr,
// not WebRTCAddr. The mesh is parallel infrastructure consumed by
// higher layers that need it (e.g. presence-gossip or app-traffic
// routing).
func (n *Node) buildMesh() error {
	if n.signaling == nil {
		return errors.New("mesh requires signaling service")
	}

	mesh := NewMeshSignaler(n.signaling, n.cfg.Logger)

	rtcTr, err := transport.NewWebRTCTransport(transport.WebRTCTransportConfig{
		Self:     n.cfg.Identity.Public().DestinationHash(),
		Signaler: mesh,
		Logger:   n.cfg.Logger,
	})
	if err != nil {
		mesh.Close()

		return fmt.Errorf("rtc transport: %w", err)
	}

	pm, err := webrtc.NewPeerManager(webrtc.PeerManagerConfig{
		Transport: rtcTr,
		Selector:  &dhtMeshSelector{n: n},
		MaxLinks:  n.cfg.MaxMeshLinks,
		Logger:    n.cfg.Logger,
	})
	if err != nil {
		_ = rtcTr.Close()
		mesh.Close()

		return fmt.Errorf("peer manager: %w", err)
	}

	n.mesh = mesh
	n.rtcTransport = rtcTr
	n.peerManager = pm

	return nil
}

// MeshTransport returns the underlying WebRTCTransport, or nil if
// mesh is disabled. Callers that want to peek the mesh inbox (e.g.
// a presence-gossip layer) hold this directly.
func (n *Node) MeshTransport() *transport.WebRTCTransport { return n.rtcTransport }

// MeshPeerManager returns the live PeerManager, or nil if mesh is
// disabled. Useful for diagnostics / observability snapshots.
func (n *Node) MeshPeerManager() *webrtc.PeerManager { return n.peerManager }

// dhtMeshSelector picks K closest peers to self from the DHT
// routing table that advertise CapCanWebRTCMesh. Self-centred
// selection is the natural fit for a resilience overlay: the closer
// a peer is in XOR, the more our routing depends on it; meshing
// with them keeps the graph densely connected exactly where it
// matters most.
//
// We over-fetch (4×k) from the routing table to give the cap-filter
// some headroom — peers without the mesh bit are silently dropped,
// so a routing-table dominated by legacy contacts still produces a
// reasonable selection. The presence resolver is consulted via the
// node's resolver; lookups that fail (cache-miss / record-expired)
// drop the contact for this tick rather than blocking reconcile —
// the next tick re-evaluates with fresher records.
type dhtMeshSelector struct {
	n *Node
}

// SelectPeers returns up to k closest contacts to self that
// advertise CapCanWebRTCMesh.
func (s *dhtMeshSelector) SelectPeers(ctx context.Context, k int) []identity.Hash {
	if s.n == nil || s.n.dht == nil {
		return nil
	}
	self := s.n.cfg.Identity.Public().DestinationHash()
	contacts := s.n.dht.Table().Closest(self, 4*k)

	out := make([]identity.Hash, 0, k)
	for _, c := range contacts {
		if len(out) >= k {
			break
		}
		// Filter self defensively — Closest should not return us, but
		// k-bucket implementations differ.
		if c.ID == self {
			continue
		}
		if !s.hasMeshCapability(ctx, c.ID) {
			continue
		}
		out = append(out, c.ID)
	}

	return out
}

// hasMeshCapability checks the local presence cache (resolver) for
// the peer's record and tests CapCanWebRTCMesh. A miss is treated
// as "not mesh-capable" (skip) — we never block on a remote DHT
// lookup here because reconcile must not stall.
func (s *dhtMeshSelector) hasMeshCapability(ctx context.Context, peer identity.Hash) bool {
	if s.n.resolver == nil {
		return false
	}

	// 250ms is the local-cache budget — if the resolver has the
	// record cached this returns instantly; otherwise a full lookup
	// would block reconcile, so we cap and treat timeout as miss.
	lctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()

	rec, err := s.n.resolver.Lookup(lctx, peer)
	if err != nil || rec == nil {
		return false
	}

	return rec.Capabilities.Has(presence.CapCanWebRTCMesh)
}

func capsFor(mode Mode, hasPublicIP, meshEnabled bool) presence.Capability {
	var caps presence.Capability
	switch mode {
	case ModeRelay:
		caps = presence.CapCanRelay | presence.CapCanBootstrap
		if hasPublicIP {
			caps |= presence.CapPublicIP | presence.CapCanSTUN | presence.CapCanTURN
		}
	default:
		// ModeClient
		if hasPublicIP {
			caps = presence.CapPublicIP
		}
	}
	if meshEnabled {
		caps |= presence.CapCanWebRTCMesh
	}

	return caps
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
	// STUN/TURN/mesh are auxiliary roles — a failure must not collapse
	// the DHT/signaling loops, so we log and continue rather than letting
	// errgroup propagate the error. Run() returning early is tantamount
	// to that role being offline; the rest of the node stays up.
	if n.stunSrv != nil {
		n.runAuxiliary(g, gctx, "STUN server", n.stunSrv.Run)
	}
	if n.turnSrv != nil {
		n.runAuxiliary(g, gctx, "TURN server", n.turnSrv.Run)
	}
	if n.rtcTransport != nil {
		n.runAuxiliary(g, gctx, "rtc transport", n.rtcTransport.Run)
	}
	if n.peerManager != nil {
		n.runAuxiliary(g, gctx, "peer manager", n.peerManager.Run)
	}

	err := g.Wait()
	closeErr := n.closeServers()
	n.pumpWg.Wait()
	// Wrapper's atomic+RWMutex coordination makes this safe against
	// late signaling-handler senders that outlive pumpWg + signaling.Close.
	n.income.Close()

	return errors.Join(err, closeErr)
}

// runAuxiliary spawns run inside g, logging non-cancel exits at Warn.
// Used to share the wrapper around every best-effort role (STUN, TURN,
// rtc transport, peer manager) that runs alongside DHT/signaling.
func (n *Node) runAuxiliary(g *errgroup.Group, ctx context.Context, name string, run func(context.Context) error) {
	g.Go(func() error {
		if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			n.cfg.Logger.Warn("network: "+name+" exited with error", "err", err)
		}

		return nil
	})
}

func (n *Node) bootstrapAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, addr := range n.cfg.Bootstrap {
		wg.Go(func() {
			_ = n.bootstrapOne(ctx, addr)
		})
	}
	wg.Wait()
}

// Bootstrap dials addr and runs a DHT bootstrap against it. Hot-reload
// entry point for the webui Settings → Bootstrap "Reconnect" / "Add"
// flows. Errors are not fatal to the running node — the caller surfaces
// them to the user. The seen-peers cache and bootstrap-overrides status
// are updated as a side effect, identical to startup-time bootstrap.
func (n *Node) Bootstrap(ctx context.Context, addr string) error {
	return n.bootstrapOne(ctx, addr)
}

// bootstrapOne is the single-address bootstrap path shared by startup
// (bootstrapAll) and runtime "Reconnect" (Bootstrap). On success: the
// address is recorded in the seen-peers cache and marked "ok" in the
// override store. On failure: the address is dropped from the seen-
// peers cache (so we stop wasting startup time on dead entries) and
// marked "fail" in the override store. Override-store calls are no-ops
// for addresses the user has not added — the storage layer silently
// ignores unknown rows.
func (n *Node) bootstrapOne(ctx context.Context, addr string) error {
	now := n.cfg.Now()
	peer, err := n.transport.Dial(addr)
	if err != nil {
		n.cfg.Logger.Warn("network: bootstrap parse", "addr", addr, "err", err)
		n.markBootstrapStatus(ctx, addr, "fail", now)

		return fmt.Errorf("network: bootstrap parse %q: %w", addr, err)
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
		n.markBootstrapStatus(ctx, addr, "fail", now)

		return fmt.Errorf("network: bootstrap %q: %w", addr, err)
	}

	n.cfg.Logger.Info("network: bootstrap ok", "addr", addr)
	if n.cfg.SeenPeerStore != nil {
		if recErr := n.cfg.SeenPeerStore.RecordSeenPeer(ctx, addr); recErr != nil {
			n.cfg.Logger.Debug("network: record seen peer", "addr", addr, "err", recErr)
		}
	}
	n.markBootstrapStatus(ctx, addr, "ok", now)

	return nil
}

func (n *Node) markBootstrapStatus(ctx context.Context, addr, status string, when time.Time) {
	if n.cfg.BootstrapOverrideStore == nil {
		return
	}
	if err := n.cfg.BootstrapOverrideStore.MarkBootstrapStatus(ctx, addr, status, when); err != nil {
		n.cfg.Logger.Debug("network: mark bootstrap status", "addr", addr, "err", err)
	}
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
	closeWith := func(label string, close func() error) {
		if err := close(); err != nil {
			joined = errors.Join(joined, fmt.Errorf("network: %s close: %w", label, err))
		}
	}

	n.closeOnce.Do(func() {
		// Mesh stack first: PeerManager owns the dial loop, the
		// WebRTCTransport owns PeerSessions, MeshSignaler holds the
		// service-level mesh handler registration. Tear them down
		// before signaling so in-flight Connects unwind cleanly.
		if n.peerManager != nil {
			closeWith("peer manager", n.peerManager.Close)
		}
		if n.rtcTransport != nil {
			closeWith("rtc transport", n.rtcTransport.Close)
		}
		if n.mesh != nil {
			n.mesh.Close()
		}
		// signaling.Service.Close is void by contract — it tears down
		// channels best-effort and returns nothing to aggregate.
		n.signaling.Close()
		if n.stunSrv != nil {
			closeWith("stun", n.stunSrv.Close)
		}
		if n.turnSrv != nil {
			closeWith("turn", n.turnSrv.Close)
		}
		closeWith("transport", n.transport.Close)
	})

	return joined
}

// LocalAddress returns the bound UDP address — useful in tests / logs.
func (n *Node) LocalAddress() string { return n.transport.LocalAddr().String() }

// Stats is a read-only snapshot of internal counters surfaced to the
// webui Settings → Network → Status panel. Cheap to compute — does no
// IO and holds no locks across boundaries.
//
// The RTC* fields are zero when MeshEnabled is false; the webui can
// hide the WebRTC mesh block via that signal.
type Stats struct {
	RoutingTableSize int
	ActiveSessions   int

	RTCPeers           int
	RTCBytesSent       int64
	RTCBytesRecv       int64
	RTCConnectAttempts int64
	RTCConnectFailures int64
	RTCICERestarts     int64
}

// Stats returns a fresh Stats snapshot.
func (n *Node) Stats() Stats {
	n.sessMu.RLock()
	active := len(n.sessions)
	n.sessMu.RUnlock()

	out := Stats{
		RoutingTableSize: n.dht.Table().Size(),
		ActiveSessions:   active,
	}
	if n.rtcTransport != nil {
		rtcStats := n.rtcTransport.Stats()
		out.RTCPeers = rtcStats.Peers
		out.RTCBytesSent = rtcStats.BytesSent
		out.RTCBytesRecv = rtcStats.BytesRecv
		out.RTCConnectAttempts = rtcStats.ConnectAttempts
		out.RTCConnectFailures = rtcStats.ConnectFailures
		out.RTCICERestarts = rtcStats.ICERestarts
	}

	return out
}

// Identity returns the node's identity. Higher layers need it to sign
// application-level envelopes (the network signs only its own presence
// records).
func (n *Node) Identity() *identity.Identity { return n.cfg.Identity }

// Income returns the channel of low-level inbound events. ONE consumer
// is supported (the messenger). The channel closes after Run returns.
func (n *Node) Income() <-chan *Income { return n.income.Recv() }

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

	if !n.income.Send(inc, n.runContext().Done()) {
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

		if !n.income.Send(inc, rctx.Done()) {
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

	if !n.income.Send(inc, n.runContext().Done()) {
		inc.Release()
	}
}

// runContext returns the ctx passed to Run, or Background if Run
// hasn't started yet (defensive — handlers should not fire before Run,
// but signaling can technically deliver a packet between Open and Run
// if the transport already saw one).
func (n *Node) runContext() context.Context {
	n.runMu.RLock()
	defer n.runMu.RUnlock()

	if n.runCtx == nil {
		return context.Background()
	}

	return n.runCtx
}

// resolverDelegate routes signaling's AddressResolver lookups through
// our presence.Resolver. Defined here (not as a closure) so the
// signaling.Service field is a stable interface reference.
type resolverDelegate struct{ n *Node }

func (r *resolverDelegate) Lookup(ctx context.Context, peer identity.PeerID) (*presence.Record, error) {
	return r.n.resolver.Lookup(ctx, peer)
}
