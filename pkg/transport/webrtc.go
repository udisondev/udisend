package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	udwebrtc "github.com/udisondev/udisend/pkg/webrtc"
)

// WebRTC transport sentinel errors.
//
// ErrNoRoute is returned by Send when the destination peer has no
// established DataChannel — strict semantics, no on-demand setup
// in the hot path. Callers (typically the network Router) should
// fall back to a different transport (UDP) or trigger an explicit
// Connect via the PeerManager.
//
// ErrAlreadyConnected is returned by Connect when a session for the
// given peer already exists. The error is not fatal; callers may
// treat it as a no-op.
//
// ErrBufferFull is returned by Send when the underlying SCTP
// DataChannel's bufferedAmount has crossed the high-water mark. The
// caller should drop or queue the frame; we never silently lose data.
//
// ErrInvalidAddr is returned when an address fails to parse as the
// WebRTC scheme.
var (
	ErrNoRoute          = errors.New("transport: no rtc route to peer")
	ErrAlreadyConnected = errors.New("transport: rtc peer already connected")
	ErrBufferFull       = errors.New("transport: rtc data channel buffer full")
	ErrInvalidAddr      = errors.New("transport: invalid rtc address")
)

// rtcScheme is the network scheme of a WebRTCAddr. It mirrors the role
// of "tcp" / "udp" / "mem" in their respective transports — kept short
// because it goes into every Packet.From.Network() call site.
const rtcScheme = "rtc"

// connectTimeoutDefault caps how long Connect waits for the
// DataChannel to open before giving up. Real ICE handshakes complete
// in <1s on a healthy LAN; 30s leaves headroom for STUN-mediated NAT
// punching with a single retry.
const connectTimeoutDefault = 30 * time.Second

// MeshSDPKind discriminates the three mesh-handshake message types
// exchanged between two network nodes to bring up a WebRTC
// DataChannel. Values mirror the InnerMesh* constants in pkg/signaling
// at the wire layer; we keep a transport-package-local enum so the
// pkg/transport API does not depend on pkg/signaling.
type MeshSDPKind byte

// MeshSDP* identify the three message kinds.
const (
	MeshSDPOffer     MeshSDPKind = 1
	MeshSDPAnswer    MeshSDPKind = 2
	MeshSDPCandidate MeshSDPKind = 3
)

// String renders the kind as a readable label for logs / errors.
func (k MeshSDPKind) String() string {
	switch k {
	case MeshSDPOffer:
		return "offer"
	case MeshSDPAnswer:
		return "answer"
	case MeshSDPCandidate:
		return "candidate"
	default:
		return "unknown"
	}
}

// MeshSDPMsg is one inbound mesh-handshake event delivered by a
// Signaler.
type MeshSDPMsg struct {
	Peer identity.Hash
	Kind MeshSDPKind
	SDP  []byte
}

// Signaler is the port WebRTCTransport uses to exchange mesh
// SDP/ICE messages with peers via the existing signaling.Service /
// Noise XK channel infrastructure. Implementations are provided by
// pkg/network — see pkg/network.MeshSignaler. Defining the interface
// here (rather than in pkg/network) lets pkg/transport stay free of
// signaling/network imports while still expressing the contract its
// peer-management code needs.
//
// SendMeshSDP is responsible for opening or reusing a signaling
// Channel to peer, encrypting `sdp` under that Channel's Noise key,
// and shipping it as an InnerMesh{Offer,Answer,Candidate} envelope.
// It MUST return only after the wire send succeeds (or fails) — the
// caller treats the post-return state as authoritative for retry.
//
// RecvMeshSDP returns a stream of inbound mesh events. The channel
// SHOULD have a small buffer so a slow consumer cannot deadlock the
// signaling dispatch goroutine; implementations drop or block by
// their own policy. Closing the channel on shutdown is OPTIONAL —
// most consumers gate on context instead.
type Signaler interface {
	SendMeshSDP(ctx context.Context, peer identity.Hash, kind MeshSDPKind, sdp []byte) error
	RecvMeshSDP() <-chan MeshSDPMsg
}

// WebRTCAddr identifies a peer on the WebRTC mesh. It is a peer-keyed
// address, not a host:port — the underlying transport (pion's ICE
// agent) is responsible for actually finding the peer's network
// endpoint via signaling and STUN/TURN candidates.
//
// The textual form is "rtc:<hex16>" where hex16 is the 32-character
// hex encoding of identity.Hash (16 bytes).
type WebRTCAddr struct {
	peer identity.Hash
}

// NewWebRTCAddr returns the WebRTCAddr for the given peer hash.
func NewWebRTCAddr(h identity.Hash) WebRTCAddr {
	return WebRTCAddr{peer: h}
}

// Peer returns the destination hash this address points at.
func (a WebRTCAddr) Peer() identity.Hash { return a.peer }

// Network reports the address scheme.
func (a WebRTCAddr) Network() string { return rtcScheme }

// String returns the canonical "rtc:<hex16>" form.
func (a WebRTCAddr) String() string {
	return rtcScheme + ":" + hex.EncodeToString(a.peer[:])
}

// ParseWebRTCAddr parses a textual "rtc:<hex16>" address.
func ParseWebRTCAddr(s string) (WebRTCAddr, error) {
	rest, ok := strings.CutPrefix(s, rtcScheme+":")
	if !ok {
		return WebRTCAddr{}, ErrInvalidAddr
	}
	if len(rest) != 2*identity.HashSize {
		return WebRTCAddr{}, ErrInvalidAddr
	}
	raw, err := hex.DecodeString(rest)
	if err != nil {
		return WebRTCAddr{}, ErrInvalidAddr
	}
	var h identity.Hash
	copy(h[:], raw)

	return WebRTCAddr{peer: h}, nil
}

// WebRTCTransportConfig parameterises NewWebRTCTransport.
type WebRTCTransportConfig struct {
	// Self is the local node's destination hash, returned by
	// LocalAddr() and used to fill the From of inbound Packets.
	Self identity.Hash

	// Signaler bridges to the wider signaling layer (Noise XK
	// envelopes). MUST be non-nil — without it the transport cannot
	// establish or accept WebRTC connections.
	Signaler Signaler

	// ICEServers configures the underlying pion PeerConnections.
	// Empty means host-only candidates (LAN testing); production
	// callers populate from presence STUN/TURN volunteers.
	ICEServers []udwebrtc.ICEServer

	// Logger receives debug / warn diagnostics. nil → slog.Default().
	Logger *slog.Logger

	// ConnectTimeout caps Connect's wait on the DataChannel opening.
	// Zero uses connectTimeoutDefault (30s).
	ConnectTimeout time.Duration
}

// WebRTCTransport implements transport.Transport over pion/webrtc
// DataChannels. Each peer is reached via its identity.Hash; the
// underlying ICE agent handles NAT traversal through volunteer STUN /
// TURN nodes. Send semantics are strict — there is no implicit
// dial-on-Send. Use Connect to bring up a DataChannel; the
// PeerManager (pkg/webrtc) drives this proactively for K mesh-links.
type WebRTCTransport struct {
	self     identity.Hash
	signaler Signaler
	logger   *slog.Logger
	iceCfg   []udwebrtc.ICEServer
	connectTimeout time.Duration

	inbox chan Packet

	mu      sync.Mutex
	peers   map[identity.Hash]*peerEntry
	pending map[identity.Hash]*pendingDial

	closeOnce sync.Once
	closed    chan struct{}
	wg        sync.WaitGroup
}

// peerEntry holds an open PeerSession plus the goroutine that pumps
// its inbox into the transport's shared inbox.
type peerEntry struct {
	sess *udwebrtc.PeerSession
}

// pendingDial coordinates a Connect-in-flight so concurrent callers
// share one underlying handshake (singleflight).
type pendingDial struct {
	done chan struct{}
	err  error
}

// NewWebRTCTransport constructs a transport. The caller MUST call
// Run(ctx) for the transport to actually accept inbound mesh
// envelopes; until then Connect/Send work but no responder-side
// PeerSessions are created.
func NewWebRTCTransport(cfg WebRTCTransportConfig) (*WebRTCTransport, error) {
	if cfg.Signaler == nil {
		return nil, errors.New("transport: WebRTCTransport requires Signaler")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = connectTimeoutDefault
	}

	return &WebRTCTransport{
		self:           cfg.Self,
		signaler:       cfg.Signaler,
		logger:         logger,
		iceCfg:         cfg.ICEServers,
		connectTimeout: timeout,
		inbox:          make(chan Packet, 256),
		peers:          make(map[identity.Hash]*peerEntry),
		pending:        make(map[identity.Hash]*pendingDial),
		closed:         make(chan struct{}),
	}, nil
}

// Run pumps inbound mesh envelopes from Signaler.RecvMeshSDP into the
// matching PeerSession. It blocks until ctx is cancelled or the
// transport is closed; when it returns, no further responder-side
// sessions are created (existing ones live until Close).
func (t *WebRTCTransport) Run(ctx context.Context) error {
	in := t.signaler.RecvMeshSDP()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.closed:
			return nil
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			t.handleInbound(ctx, msg)
		}
	}
}

// LocalAddr returns the WebRTCAddr for this transport's identity.
func (t *WebRTCTransport) LocalAddr() net.Addr { return WebRTCAddr{peer: t.self} }

// Dial parses a textual "rtc:<hex16>" address. It does not contact
// the peer — Connect handles dialing.
func (t *WebRTCTransport) Dial(addr string) (net.Addr, error) {
	return ParseWebRTCAddr(addr)
}

// Send writes payload to the open DataChannel for `to`. Returns
// ErrNoRoute if no session exists.
func (t *WebRTCTransport) Send(_ context.Context, to net.Addr, payload []byte) error {
	if t.isClosed() {
		return ErrClosed
	}

	addr, ok := to.(WebRTCAddr)
	if !ok {
		return fmt.Errorf("transport: rtc got %T, want WebRTCAddr", to)
	}

	t.mu.Lock()
	entry, ok := t.peers[addr.peer]
	t.mu.Unlock()
	if !ok {
		return ErrNoRoute
	}

	if err := entry.sess.Send(payload); err != nil {
		// Map udwebrtc's specific errors back to transport's API.
		switch {
		case errors.Is(err, udwebrtc.ErrBufferFull):
			return ErrBufferFull
		case errors.Is(err, udwebrtc.ErrPayloadLarge):
			return fmt.Errorf("transport: %w (max %d bytes)",
				err, udwebrtc.MaxMessageSize)
		case errors.Is(err, udwebrtc.ErrNotOpen),
			errors.Is(err, udwebrtc.ErrClosed):
			return ErrNoRoute
		default:
			return err
		}
	}

	return nil
}

// Inbox delivers received packets. The channel is closed when the
// transport is Closed.
func (t *WebRTCTransport) Inbox() <-chan Packet { return t.inbox }

// IsConnected reports whether peer currently has an open
// DataChannel-backed session. PeerManager (pkg/webrtc) polls this
// to detect drops between Connect attempts.
func (t *WebRTCTransport) IsConnected(peer identity.Hash) bool {
	t.mu.Lock()
	_, ok := t.peers[peer]
	t.mu.Unlock()

	return ok
}

// Peers returns a snapshot of currently-connected peer hashes.
// Useful for diagnostics and PeerManager bookkeeping.
func (t *WebRTCTransport) Peers() []identity.Hash {
	t.mu.Lock()
	out := make([]identity.Hash, 0, len(t.peers))
	for h := range t.peers {
		out = append(out, h)
	}
	t.mu.Unlock()

	return out
}

// Disconnect tears down a session for peer. Used by PeerManager to
// drop healthy-but-evicted peers when reshaping the mesh. Returns
// false if there was no session to drop.
func (t *WebRTCTransport) Disconnect(peer identity.Hash) bool {
	t.mu.Lock()
	entry, ok := t.peers[peer]
	delete(t.peers, peer)
	t.mu.Unlock()
	if !ok {
		return false
	}
	_ = entry.sess.Close()

	return true
}

// Close releases all sessions and signals waiters. Idempotent.
func (t *WebRTCTransport) Close() error {
	t.closeOnce.Do(func() {
		close(t.closed)

		t.mu.Lock()
		for _, e := range t.peers {
			_ = e.sess.Close()
		}
		t.peers = make(map[identity.Hash]*peerEntry)
		// Wake any pending Connect waiters with a Closed error.
		for _, p := range t.pending {
			p.err = ErrClosed
			close(p.done)
		}
		t.pending = make(map[identity.Hash]*pendingDial)
		t.mu.Unlock()

		t.wg.Wait()
		close(t.inbox)
	})

	return nil
}

// Connect brings up a DataChannel to peer. It is idempotent and
// singleflight: concurrent calls to the same peer share one
// underlying handshake. Returns nil once the DataChannel is open or
// an error if the handshake fails / context cancels.
func (t *WebRTCTransport) Connect(ctx context.Context, peer identity.Hash) error {
	if t.isClosed() {
		return ErrClosed
	}

	t.mu.Lock()
	if _, ok := t.peers[peer]; ok {
		t.mu.Unlock()
		return nil
	}
	if pd, ok := t.pending[peer]; ok {
		t.mu.Unlock()
		select {
		case <-pd.done:
			return pd.err
		case <-ctx.Done():
			return ctx.Err()
		case <-t.closed:
			return ErrClosed
		}
	}
	pd := &pendingDial{done: make(chan struct{})}
	t.pending[peer] = pd
	t.mu.Unlock()

	err := t.dial(ctx, peer)

	t.mu.Lock()
	pd.err = err
	close(pd.done)
	delete(t.pending, peer)
	t.mu.Unlock()

	return err
}

// dial creates an initiator-side PeerSession, fires the offer through
// the signaler, and waits until the DataChannel opens. The session
// MUST be installed in t.peers BEFORE CreateOffer fires the SDP
// emission: the answer / ICE candidates from the remote arrive on
// the signaler's inbound stream and look the session up by peer hash,
// so an empty t.peers entry would drop them silently. On any error
// path we de-install before returning.
func (t *WebRTCTransport) dial(ctx context.Context, peer identity.Hash) error {
	deadlineCtx, cancel := context.WithTimeout(ctx, t.connectTimeout)
	defer cancel()

	sess, err := t.openSession(peer)
	if err != nil {
		return err
	}
	t.installPeer(peer, sess)

	if err := sess.CreateOffer(deadlineCtx); err != nil {
		t.removePeer(peer)
		return fmt.Errorf("transport: rtc create offer: %w", err)
	}

	select {
	case <-sess.Opened():
		return nil
	case <-sess.Closed():
		t.removePeer(peer)
		return errors.New("transport: rtc session closed during dial")
	case <-deadlineCtx.Done():
		t.removePeer(peer)
		return fmt.Errorf("transport: rtc connect: %w", deadlineCtx.Err())
	case <-t.closed:
		t.removePeer(peer)
		return ErrClosed
	}
}

// removePeer evicts a peer entry and closes its session. Used on dial
// failure paths to clean up the eager install.
func (t *WebRTCTransport) removePeer(peer identity.Hash) {
	t.mu.Lock()
	entry, ok := t.peers[peer]
	delete(t.peers, peer)
	t.mu.Unlock()
	if ok {
		_ = entry.sess.Close()
	}
}

// handleInbound drives one inbound mesh envelope from the signaler:
// offers create responder sessions, answers feed back into the
// initiator-side session, and candidates are trickled into whatever
// session is associated with the peer.
func (t *WebRTCTransport) handleInbound(ctx context.Context, msg MeshSDPMsg) {
	switch msg.Kind {
	case MeshSDPOffer:
		t.handleOffer(ctx, msg.Peer, msg.SDP)
	case MeshSDPAnswer:
		t.handleAnswer(ctx, msg.Peer, msg.SDP)
	case MeshSDPCandidate:
		t.handleCandidate(msg.Peer, string(msg.SDP))
	default:
		t.logger.Debug("rtc: unknown mesh kind", "peer", msg.Peer, "kind", msg.Kind)
	}
}

func (t *WebRTCTransport) handleOffer(ctx context.Context, peer identity.Hash, sdp []byte) {
	t.mu.Lock()
	if _, ok := t.peers[peer]; ok {
		t.mu.Unlock()
		t.logger.Debug("rtc: duplicate offer from connected peer", "peer", peer)
		return
	}
	t.mu.Unlock()

	sess, err := t.openSession(peer)
	if err != nil {
		t.logger.Warn("rtc: open responder session", "peer", peer, "err", err)
		return
	}

	if err := sess.AcceptOffer(ctx, sdp); err != nil {
		t.logger.Warn("rtc: accept offer", "peer", peer, "err", err)
		_ = sess.Close()
		return
	}

	// Install eagerly — the answer SDP has already been emitted via
	// onSDP, and trickle ICE may now arrive on this peer entry.
	t.installPeer(peer, sess)
}

func (t *WebRTCTransport) handleAnswer(ctx context.Context, peer identity.Hash, sdp []byte) {
	t.mu.Lock()
	entry, ok := t.peers[peer]
	t.mu.Unlock()
	if !ok {
		t.logger.Debug("rtc: answer for unknown peer", "peer", peer)
		return
	}
	if err := entry.sess.AcceptAnswer(ctx, sdp); err != nil {
		t.logger.Warn("rtc: accept answer", "peer", peer, "err", err)
	}
}

func (t *WebRTCTransport) handleCandidate(peer identity.Hash, candidate string) {
	t.mu.Lock()
	entry, ok := t.peers[peer]
	t.mu.Unlock()
	if !ok {
		t.logger.Debug("rtc: candidate for unknown peer", "peer", peer)
		return
	}
	if err := entry.sess.AddRemoteCandidate(candidate); err != nil {
		t.logger.Debug("rtc: add candidate", "peer", peer, "err", err)
	}
}

// openSession constructs a PeerSession with onSDP / onICE wired into
// the signaler. Caller is responsible for installing the session in
// t.peers once it is appropriate (initiator: after Opened; responder:
// after AcceptOffer).
func (t *WebRTCTransport) openSession(peer identity.Hash) (*udwebrtc.PeerSession, error) {
	cfg := udwebrtc.PeerSessionConfig{
		Peer:       peer,
		ICEServers: t.iceCfg,
		Logger:     t.logger,
		OnSDP: func(kind udwebrtc.SDPKind, sdp []byte) {
			t.fireSDP(peer, kind, sdp)
		},
		OnICE: func(candidate string) {
			t.fireICE(peer, candidate)
		},
	}

	sess, err := udwebrtc.NewPeerSession(cfg)
	if err != nil {
		return nil, fmt.Errorf("transport: rtc new session: %w", err)
	}

	return sess, nil
}

// installPeer registers sess under peer and starts the inbox-pump
// goroutine that fans its Recv() into the transport's inbox. If a
// peer is already installed, the new session is closed and the old
// one wins (concurrent dial race).
func (t *WebRTCTransport) installPeer(peer identity.Hash, sess *udwebrtc.PeerSession) {
	t.mu.Lock()
	if _, ok := t.peers[peer]; ok {
		t.mu.Unlock()
		_ = sess.Close()
		return
	}
	t.peers[peer] = &peerEntry{sess: sess}
	t.mu.Unlock()

	t.wg.Add(1)
	go t.pumpInbox(peer, sess)
}

// pumpInbox forwards messages from a single PeerSession to the
// transport's shared inbox. Exits when the session closes or the
// transport is shutting down.
func (t *WebRTCTransport) pumpInbox(peer identity.Hash, sess *udwebrtc.PeerSession) {
	defer t.wg.Done()

	addr := WebRTCAddr{peer: peer}
	for {
		select {
		case payload, ok := <-sess.Recv():
			if !ok {
				return
			}
			pkt := Packet{From: addr, Payload: payload}
			select {
			case t.inbox <- pkt:
			case <-t.closed:
				return
			}
		case <-sess.Closed():
			return
		case <-t.closed:
			return
		}
	}
}

// fireSDP routes outbound SDPs from a PeerSession through the
// signaler. Runs on a pion goroutine — must not block.
func (t *WebRTCTransport) fireSDP(peer identity.Hash, kind udwebrtc.SDPKind, sdp []byte) {
	var meshKind MeshSDPKind
	switch kind {
	case udwebrtc.SDPOffer:
		meshKind = MeshSDPOffer
	case udwebrtc.SDPAnswer:
		meshKind = MeshSDPAnswer
	default:
		t.logger.Debug("rtc: unknown SDP kind", "peer", peer, "kind", kind)
		return
	}

	// Fire-and-forget on a fresh goroutine: SendMeshSDP may block on
	// the signaling channel, and we are running on a pion-internal
	// goroutine. tracked by t.wg so Close drains us.
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()

		ctx, cancel := context.WithTimeout(context.Background(), t.connectTimeout)
		defer cancel()
		if err := t.signaler.SendMeshSDP(ctx, peer, meshKind, sdp); err != nil {
			t.logger.Warn("rtc: signaler send sdp", "peer", peer, "kind", meshKind, "err", err)
		}
	}()
}

// fireICE routes outbound ICE candidates through the signaler. Same
// non-blocking discipline as fireSDP.
func (t *WebRTCTransport) fireICE(peer identity.Hash, candidate string) {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()

		ctx, cancel := context.WithTimeout(context.Background(), t.connectTimeout)
		defer cancel()
		if err := t.signaler.SendMeshSDP(ctx, peer, MeshSDPCandidate, []byte(candidate)); err != nil {
			t.logger.Debug("rtc: signaler send ice", "peer", peer, "err", err)
		}
	}()
}

func (t *WebRTCTransport) isClosed() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

// Compile-time assertion that WebRTCTransport satisfies Transport.
var _ Transport = (*WebRTCTransport)(nil)
