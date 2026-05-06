package webrtc

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
)

// MeshTransport is the slice of WebRTCTransport that PeerManager
// consumes. Defining the contract here lets us swap in fakes for
// unit-testing the FSM without spinning up real pion sessions.
//
// Inputs accept identity.PeerID so external Suite-aware peer types
// can drive the manager. Peers() returns []identity.Hash because the
// transport stores its connection map keyed by Hash — exposing that
// internal type avoids copies on the read path.
type MeshTransport interface {
	Connect(ctx context.Context, peer identity.PeerID) error
	IsConnected(peer identity.PeerID) bool
	Peers() []identity.Hash
	Disconnect(peer identity.PeerID) bool
}

// PeerSelector returns up to k candidate peers ordered by
// desirability — closest by XOR ∪ random subnet-diverse picks. The
// selector is consulted on every PeerManager tick; implementations
// may return live snapshots from a routing table.
type PeerSelector interface {
	SelectPeers(ctx context.Context, k int) []identity.Hash
}

// PeerSelectorFunc adapts a function to the PeerSelector interface.
type PeerSelectorFunc func(ctx context.Context, k int) []identity.Hash

// SelectPeers calls the underlying function.
func (f PeerSelectorFunc) SelectPeers(ctx context.Context, k int) []identity.Hash {
	return f(ctx, k)
}

// PeerManager-level errors.
var (
	ErrManagerClosed = errors.New("webrtc: peer manager closed")
)

// Defaults for PeerManagerConfig.
const (
	// DefaultMaxLinks targets eight live mesh links per node — the
	// resilience benefit (K-2 losses tolerated before a node is
	// disconnected from the overlay) without runaway PeerConnection
	// memory cost.
	DefaultMaxLinks = 8

	// DefaultTickInterval is how often the main loop reconciles the
	// connected set against the selector. Long enough to amortise
	// reconnect bursts, short enough that a peer drop is patched up
	// within a minute.
	DefaultTickInterval = 30 * time.Second

	// DefaultBackoffInitial is the first reconnect delay after a
	// failed Connect. Doubles up to BackoffMax.
	DefaultBackoffInitial = time.Second

	// DefaultBackoffMax caps the reconnect delay so a long outage
	// does not stretch into 10-minute waits.
	DefaultBackoffMax = 64 * time.Second

	// DefaultBackoffJitter percentage — full-jitter style, picking
	// a uniform delay in [delay*(1-jitter), delay*(1+jitter)].
	DefaultBackoffJitter = 0.25

	// DefaultConnectTimeout caps how long one Connect attempt may
	// take before the manager moves on (fail counts as a backoff
	// trigger). Generous to allow slow ICE / TURN paths.
	DefaultConnectTimeout = 30 * time.Second
)

// PeerManagerConfig parameterises NewPeerManager.
type PeerManagerConfig struct {
	// Transport is the WebRTC transport this manager drives.
	Transport MeshTransport

	// Selector returns the desired peer set on each tick. MUST be
	// non-nil; a nil selector is a programming error and will be
	// rejected at construction time.
	Selector PeerSelector

	// MaxLinks is the target number of live mesh-links. 0 falls back
	// to DefaultMaxLinks.
	MaxLinks int

	// TickInterval is the reconcile cadence. 0 falls back to
	// DefaultTickInterval.
	TickInterval time.Duration

	// BackoffInitial / BackoffMax / BackoffJitter parameterise the
	// per-peer reconnect schedule. Zero values fall back to defaults.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	BackoffJitter  float64

	// ConnectTimeout caps one Connect attempt. 0 → DefaultConnectTimeout.
	ConnectTimeout time.Duration

	// Logger. nil → slog.Default().
	Logger *slog.Logger

	// Now is the clock used for backoff arithmetic. Tests substitute
	// a synctest-friendly clock; production callers leave nil.
	Now func() time.Time
}

// peerState tracks one candidate's lifecycle.
type peerState int

const (
	stateDisconnected peerState = 0
	stateConnecting   peerState = 1
	stateConnected    peerState = 2
)

func (s peerState) String() string {
	switch s {
	case stateDisconnected:
		return "disconnected"
	case stateConnecting:
		return "connecting"
	case stateConnected:
		return "connected"
	default:
		return "unknown"
	}
}

// peerEntry is one peer's bookkeeping inside the manager.
type peerEntry struct {
	state          peerState
	failures       int
	nextAttemptAt  time.Time
}

// PeerManager maintains K live mesh-links by reconciling its
// connected set against a PeerSelector on every tick. Reconnects
// follow exponential backoff with jitter; the FSM also serves as the
// data structure exposed to observability.
type PeerManager struct {
	cfg PeerManagerConfig

	mu      sync.Mutex
	entries map[identity.Hash]*peerEntry

	closeOnce sync.Once
	closed    chan struct{}
	wakeup    chan struct{}
}

// NewPeerManager constructs a manager. Returns an error on missing
// required dependencies.
func NewPeerManager(cfg PeerManagerConfig) (*PeerManager, error) {
	if cfg.Transport == nil {
		return nil, errors.New("webrtc: PeerManager requires Transport")
	}
	if cfg.Selector == nil {
		return nil, errors.New("webrtc: PeerManager requires Selector")
	}
	if cfg.MaxLinks <= 0 {
		cfg.MaxLinks = DefaultMaxLinks
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = DefaultTickInterval
	}
	if cfg.BackoffInitial <= 0 {
		cfg.BackoffInitial = DefaultBackoffInitial
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = DefaultBackoffMax
	}
	if cfg.BackoffJitter <= 0 {
		cfg.BackoffJitter = DefaultBackoffJitter
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	return &PeerManager{
		cfg:     cfg,
		entries: make(map[identity.Hash]*peerEntry),
		closed:  make(chan struct{}),
		wakeup:  make(chan struct{}, 1),
	}, nil
}

// Run drives the reconcile loop until ctx is cancelled. Returns
// ctx.Err() on graceful exit; never returns nil except after Close.
func (pm *PeerManager) Run(ctx context.Context) error {
	ticker := time.NewTicker(pm.cfg.TickInterval)
	defer ticker.Stop()

	// Reconcile once immediately so the first K peers come up without
	// waiting a full tick.
	pm.reconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pm.closed:
			return nil
		case <-ticker.C:
			pm.reconcile(ctx)
		case <-pm.wakeup:
			pm.reconcile(ctx)
		}
	}
}

// Close stops the manager. Idempotent.
func (pm *PeerManager) Close() error {
	pm.closeOnce.Do(func() {
		close(pm.closed)
	})

	return nil
}

// PokeForReconcile asks the manager to run a reconcile pass on its
// next iteration without waiting for the tick. Useful from event
// handlers (e.g. signaling-channel up/down) to propagate state
// changes faster than TickInterval.
func (pm *PeerManager) PokeForReconcile() {
	select {
	case pm.wakeup <- struct{}{}:
	default:
		// Already pending — one wake is enough.
	}
}

// Snapshot returns a per-peer state copy for diagnostics. Not
// performance-critical; allocates.
func (pm *PeerManager) Snapshot() map[identity.Hash]string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	out := make(map[identity.Hash]string, len(pm.entries))
	for h, e := range pm.entries {
		out[h] = e.state.String()
	}

	return out
}

// reconcile is one pass through the peer table: drop stale entries
// no longer in the selector's set, mark transport-backed
// disconnections, and dial up to MaxLinks targets that are due.
func (pm *PeerManager) reconcile(ctx context.Context) {
	if pm.isClosed() {
		return
	}

	desired := pm.cfg.Selector.SelectPeers(ctx, pm.cfg.MaxLinks)
	desiredSet := make(map[identity.Hash]struct{}, len(desired))
	for _, h := range desired {
		desiredSet[h] = struct{}{}
	}

	pm.mu.Lock()

	// 1. Reconcile transport-side connectedness with our state. The
	//    transport may have dropped a peer between ticks (DC closed,
	//    ICE failed); flip the state so backoff kicks in.
	for h, e := range pm.entries {
		if e.state == stateConnected && !pm.cfg.Transport.IsConnected(h) {
			pm.cfg.Logger.Debug("peer-manager: detected drop", "peer", h)
			e.state = stateDisconnected
			e.failures++
			e.nextAttemptAt = pm.scheduleAttempt(e.failures)
		}
	}

	// 2. Drop peers no longer in the desired set (selector evicted
	//    them). If they are still connected, ask the transport to
	//    disconnect cleanly.
	for h := range pm.entries {
		if _, keep := desiredSet[h]; keep {
			continue
		}
		if pm.cfg.Transport.IsConnected(h) {
			pm.cfg.Transport.Disconnect(h)
		}
		delete(pm.entries, h)
	}

	// 3. Materialise entries for newly-desired peers.
	for _, h := range desired {
		if _, ok := pm.entries[h]; ok {
			continue
		}
		pm.entries[h] = &peerEntry{state: stateDisconnected}
	}

	// 4. Snapshot dial candidates: disconnected peers whose
	//    nextAttemptAt has elapsed. Take the lock-protected view
	//    inline so concurrent reconciles do not double-dial.
	now := pm.cfg.Now()
	connectedCount := 0
	dialList := make([]identity.Hash, 0, len(pm.entries))
	for h, e := range pm.entries {
		if e.state == stateConnected {
			connectedCount++
			continue
		}
		if e.state == stateConnecting {
			continue
		}
		if !e.nextAttemptAt.IsZero() && now.Before(e.nextAttemptAt) {
			continue
		}
		dialList = append(dialList, h)
	}

	// 5. Cap the dial set so we never exceed MaxLinks even if the
	//    selector returned more candidates than we wish to keep.
	for i, h := range dialList {
		if connectedCount+i >= pm.cfg.MaxLinks {
			dialList = dialList[:i]
			break
		}
		pm.entries[h].state = stateConnecting
	}

	pm.mu.Unlock()

	// 6. Dispatch dials concurrently. Each dial runs with its own
	//    timeout so a stuck handshake does not block reconcile.
	for _, h := range dialList {
		go pm.dial(ctx, h)
	}
}

// dial runs one Connect attempt and updates the entry on completion.
// Errors trigger backoff via the failure counter.
func (pm *PeerManager) dial(ctx context.Context, peer identity.Hash) {
	dialCtx, cancel := context.WithTimeout(ctx, pm.cfg.ConnectTimeout)
	defer cancel()

	err := pm.cfg.Transport.Connect(dialCtx, peer)

	pm.mu.Lock()
	defer pm.mu.Unlock()
	e, ok := pm.entries[peer]
	if !ok {
		// Manager dropped this peer between dispatch and completion.
		return
	}
	if err != nil {
		pm.cfg.Logger.Debug("peer-manager: dial failed", "peer", peer, "err", err)
		e.state = stateDisconnected
		e.failures++
		e.nextAttemptAt = pm.scheduleAttempt(e.failures)

		return
	}
	e.state = stateConnected
	e.failures = 0
	e.nextAttemptAt = time.Time{}
}

// scheduleAttempt returns the next attempt time for a peer that has
// failed `failures` times in a row. Doubles the base delay each
// failure, capping at BackoffMax, then applies symmetric jitter.
func (pm *PeerManager) scheduleAttempt(failures int) time.Time {
	delay := pm.cfg.BackoffInitial
	for i := 1; i < failures; i++ {
		delay *= 2
		if delay >= pm.cfg.BackoffMax {
			delay = pm.cfg.BackoffMax
			break
		}
	}
	// Symmetric jitter: pick uniform in [delay*(1-j), delay*(1+j)].
	jitter := pm.cfg.BackoffJitter
	if jitter > 0 {
		factor := 1 - jitter + rand.Float64()*(2*jitter)
		delay = time.Duration(float64(delay) * factor)
	}
	if delay < pm.cfg.BackoffInitial/2 {
		delay = pm.cfg.BackoffInitial / 2
	}

	return pm.cfg.Now().Add(delay)
}

func (pm *PeerManager) isClosed() bool {
	select {
	case <-pm.closed:
		return true
	default:
		return false
	}
}
