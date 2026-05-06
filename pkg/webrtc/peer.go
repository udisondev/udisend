package webrtc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	pion "github.com/pion/webrtc/v4"

	"github.com/udisondev/udisend/pkg/identity"
)

// MaxMessageSize caps the bytes that may be Send()'d in one call.
// Mirrors signaling/wire's 64 KiB ceiling and pion's safe SCTP limit
// for unfragmented messages — beyond this pion may drop or refuse the
// frame depending on the negotiated maxMessageSize attribute.
const MaxMessageSize = 64 * 1024

// BufferedAmountHighWater is the SCTP send-buffer threshold at which
// Send returns ErrBufferFull. Picked at 1 MiB so a slow peer cannot
// pin the local node's RAM while keeping room for one DC chunk per
// concurrent operation.
const BufferedAmountHighWater = 1024 * 1024

// BufferedAmountLowThreshold is the watermark at which the SCTP layer
// fires OnBufferedAmountLow, unblocking writers that hit the high
// watermark. We aim for half of the high mark so the writer doesn't
// thrash between blocked and free.
const BufferedAmountLowThreshold = BufferedAmountHighWater / 2

// dataChannelLabel is the label pion uses to identify the DataChannel
// on both sides. The label is local to this package; remote peers
// match it by ordering / negotiated-id, not by string.
const dataChannelLabel = "udisend-mesh"

// inboxBuffer bounds how many messages we hold from the wire before
// the application drains Recv(). 256 matches signaling.Channel and is
// well above any realistic DHT/signaling burst.
const inboxBuffer = 256

// connectTimeout is how long Send / Recv consumers wait for the
// underlying DataChannel to open before returning an error. Distinct
// from the application-level context: the application may have its
// own deadline.
const connectTimeout = 30 * time.Second

// SDPKind discriminates the offer / answer flavours surfaced through
// PeerSessionConfig.OnSDP. Trickle ICE candidates are surfaced
// separately via OnICE.
type SDPKind byte

// SDPOffer / SDPAnswer cover the pion SessionDescription Type field.
const (
	SDPOffer SDPKind = iota + 1
	SDPAnswer
)

// String renders SDPKind for log lines.
func (k SDPKind) String() string {
	switch k {
	case SDPOffer:
		return "offer"
	case SDPAnswer:
		return "answer"
	default:
		return "unknown"
	}
}

// PeerSession-level errors.
var (
	ErrNotOpen      = errors.New("webrtc: data channel not open")
	ErrClosed       = errors.New("webrtc: peer session closed")
	ErrBufferFull   = errors.New("webrtc: send buffer full")
	ErrPayloadLarge = errors.New("webrtc: payload exceeds max message size")
)

// ICEServer is a configuration for a single STUN / TURN endpoint. It
// mirrors pion/webrtc.ICEServer but is reproduced here so callers in
// pkg/transport / pkg/webrtc do not need to import pion's package
// types.
type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// PeerSessionConfig parameterises NewPeerSession. The caller MUST set
// Peer; everything else has reasonable zero-value defaults.
//
// OnSDP fires when this session emits an offer or answer; the byte
// slice is the raw SDP from pion (UTF-8). OnICE fires for each
// trickled candidate; the string is the candidate-line ("candidate:1
// 1 udp ..."). Both callbacks run on pion's internal goroutines —
// implementations MUST not block longer than necessary; queue and
// return.
type PeerSessionConfig struct {
	Peer       identity.Hash
	ICEServers []ICEServer
	OnSDP      func(kind SDPKind, sdp []byte)
	OnICE      func(candidate string)
	Logger     *slog.Logger
}

// PeerSession owns one pion PeerConnection and the DataChannel
// negotiated through it. Lifecycle: NewPeerSession → either
// CreateOffer (initiator) or AcceptOffer (responder) → trickle ICE
// via AddRemoteCandidate / OnICE → Send / Recv once Opened()
// closes → Close.
type PeerSession struct {
	peer    identity.Hash
	pc      *pion.PeerConnection
	dc      atomic.Pointer[pion.DataChannel]
	logger  *slog.Logger
	onSDP   func(SDPKind, []byte)
	onICE   func(string)

	inbox    chan []byte
	opened   chan struct{}
	openOnce sync.Once

	closeOnce sync.Once
	closed    chan struct{}

	bufferLow chan struct{} // unblocks Send when bufferedAmount drops
	sendMu    sync.Mutex    // serialises Send so concurrent writers don't race the high-water check

	// recvWg tracks in-flight OnMessage callbacks so Close can drain
	// them before closing inbox — see Close. Without this, a callback
	// that picked the `case p.inbox <- ...` branch right before Close
	// would race with `close(p.inbox)` and panic.
	recvWg sync.WaitGroup
}

// NewPeerSession constructs a session and wires the pion-side
// callbacks. It does NOT create the DataChannel yet — that happens in
// CreateOffer (initiator) or arrives via OnDataChannel
// (responder). Callers MUST drive one of those entry points.
func NewPeerSession(cfg PeerSessionConfig) (*PeerSession, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	pcCfg := pion.Configuration{
		ICEServers: convertICEServers(cfg.ICEServers),
	}
	pc, err := pion.NewPeerConnection(pcCfg)
	if err != nil {
		return nil, fmt.Errorf("webrtc: new peer connection: %w", err)
	}

	p := &PeerSession{
		peer:      cfg.Peer,
		pc:        pc,
		logger:    logger,
		onSDP:     cfg.OnSDP,
		onICE:     cfg.OnICE,
		inbox:     make(chan []byte, inboxBuffer),
		opened:    make(chan struct{}),
		closed:    make(chan struct{}),
		bufferLow: make(chan struct{}, 1),
	}

	pc.OnICECandidate(func(c *pion.ICECandidate) {
		// Nil signals end-of-candidates. We are using trickle, so we
		// emit each non-nil candidate but do not require explicit
		// gathering-complete signalling.
		if c == nil {
			return
		}
		if p.onICE == nil {
			return
		}
		init := c.ToJSON()
		p.onICE(init.Candidate)
	})

	pc.OnDataChannel(func(dc *pion.DataChannel) {
		// Responder path: the initiator created a DC; pion gives us
		// the local handle here. Wire it up identically to the
		// initiator-side DC.
		p.attachDataChannel(dc)
	})

	pc.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		switch state {
		case pion.PeerConnectionStateFailed,
			pion.PeerConnectionStateClosed,
			pion.PeerConnectionStateDisconnected:
			p.logger.Debug("webrtc: peer connection state", "peer", p.peer, "state", state)
		}
	})

	return p, nil
}

// Peer returns the destination hash of the remote end.
func (p *PeerSession) Peer() identity.Hash { return p.peer }

// Opened returns a channel that closes when the underlying
// DataChannel is open. Callers gate Send on this via select.
func (p *PeerSession) Opened() <-chan struct{} { return p.opened }

// Closed returns a channel that closes when the session is torn down.
func (p *PeerSession) Closed() <-chan struct{} { return p.closed }

// Recv returns the inbound message stream.
func (p *PeerSession) Recv() <-chan []byte { return p.inbox }

// CreateOffer drives the initiator path: opens a DataChannel, builds
// an SDP offer, sets it as the local description, and fires OnSDP. It
// returns once SetLocalDescription completes — pion will continue
// gathering candidates asynchronously and OnICECandidate will fire
// for each.
func (p *PeerSession) CreateOffer(_ context.Context) error {
	if p.isClosed() {
		return ErrClosed
	}

	dc, err := p.pc.CreateDataChannel(dataChannelLabel, &pion.DataChannelInit{
		Ordered: ptrOf(true),
	})
	if err != nil {
		return fmt.Errorf("webrtc: create data channel: %w", err)
	}
	p.attachDataChannel(dc)

	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("webrtc: create offer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("webrtc: set local offer: %w", err)
	}
	if p.onSDP != nil {
		p.onSDP(SDPOffer, []byte(offer.SDP))
	}

	return nil
}

// AcceptOffer drives the responder path: applies the remote offer,
// generates an answer, sets it locally, and fires OnSDP with the
// answer.
func (p *PeerSession) AcceptOffer(_ context.Context, sdp []byte) error {
	if p.isClosed() {
		return ErrClosed
	}

	if err := p.pc.SetRemoteDescription(pion.SessionDescription{
		Type: pion.SDPTypeOffer,
		SDP:  string(sdp),
	}); err != nil {
		return fmt.Errorf("webrtc: set remote offer: %w", err)
	}

	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("webrtc: create answer: %w", err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("webrtc: set local answer: %w", err)
	}
	if p.onSDP != nil {
		p.onSDP(SDPAnswer, []byte(answer.SDP))
	}

	return nil
}

// AcceptAnswer applies the remote answer to the local PeerConnection.
// Initiator-side only.
func (p *PeerSession) AcceptAnswer(_ context.Context, sdp []byte) error {
	if p.isClosed() {
		return ErrClosed
	}

	if err := p.pc.SetRemoteDescription(pion.SessionDescription{
		Type: pion.SDPTypeAnswer,
		SDP:  string(sdp),
	}); err != nil {
		return fmt.Errorf("webrtc: set remote answer: %w", err)
	}

	return nil
}

// AddRemoteCandidate trickles in one ICE candidate received from the
// peer (typically out-of-band via the signaling channel).
func (p *PeerSession) AddRemoteCandidate(candidate string) error {
	if p.isClosed() {
		return ErrClosed
	}
	if candidate == "" {
		return nil
	}

	return p.pc.AddICECandidate(pion.ICECandidateInit{Candidate: candidate})
}

// Send writes payload to the open DataChannel. Returns ErrNotOpen
// before the DC opens, ErrPayloadLarge if too big, ErrBufferFull when
// the SCTP send buffer crosses the high-water mark.
func (p *PeerSession) Send(payload []byte) error {
	if p.isClosed() {
		return ErrClosed
	}
	if len(payload) > MaxMessageSize {
		return ErrPayloadLarge
	}

	dc := p.dc.Load()
	if dc == nil || dc.ReadyState() != pion.DataChannelStateOpen {
		return ErrNotOpen
	}

	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	// Wait once if we're already past the high-water mark — readers
	// drain via OnBufferedAmountLow → bufferLow signal. We never wait
	// indefinitely; if the peer is gone the dc.Send below will fail.
	//
	// time.NewTimer + Stop instead of time.After: a Send hot path
	// hammering the high-water mark would otherwise leak one timer
	// goroutine per blocked-and-then-unblocked call (the timer stays
	// alive until natural expiry — 30 s — even after the select
	// fires on bufferLow / closed). Stop releases the timer slot
	// immediately on the unblocked paths.
	if dc.BufferedAmount() > BufferedAmountHighWater {
		timer := time.NewTimer(connectTimeout)
		select {
		case <-p.bufferLow:
			timer.Stop()
		case <-p.closed:
			timer.Stop()
			return ErrClosed
		case <-timer.C:
			return ErrBufferFull
		}
	}

	return dc.Send(payload)
}

// RestartICE asks pion to renegotiate ICE: it generates a fresh
// offer with the ICE-restart flag set (which rotates ufrag /
// password), sets it as the local description, and fires OnSDP. The
// caller ships this offer back through the signaling channel; the
// peer answers and the new ICE candidates flow as usual. There is no
// separate RestartICE method in pion/v4 — the flag on OfferOptions
// is the canonical way.
func (p *PeerSession) RestartICE() error {
	if p.isClosed() {
		return ErrClosed
	}

	offer, err := p.pc.CreateOffer(&pion.OfferOptions{ICERestart: true})
	if err != nil {
		return fmt.Errorf("webrtc: ice-restart offer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("webrtc: ice-restart set local: %w", err)
	}
	if p.onSDP != nil {
		p.onSDP(SDPOffer, []byte(offer.SDP))
	}

	return nil
}

// Close tears down the pion PeerConnection and closes the inbox so
// `for msg := range sess.Recv()` consumers exit cleanly. Idempotent.
//
// Order matters: close `closed` first so any in-flight OnMessage
// callbacks bail on the select. Then pc.Close() blocks until pion
// drains its readLoop — no new OnMessage will fire after it returns.
// recvWg.Wait() catches any callback that started Add(1)'ing before
// the readLoop exit but hasn't reached its defer Done yet. Only then
// is it safe to close inbox; an in-flight callback that picked the
// `inbox <- msg.Data` send case would otherwise race the close.
func (p *PeerSession) Close() error {
	var err error
	p.closeOnce.Do(func() {
		close(p.closed)
		err = p.pc.Close()
		p.recvWg.Wait()
		close(p.inbox)
	})

	return err
}

// attachDataChannel wires up OnOpen / OnMessage / OnError on the
// negotiated DC. Called from both initiator (after CreateDataChannel)
// and responder (via OnDataChannel).
func (p *PeerSession) attachDataChannel(dc *pion.DataChannel) {
	p.dc.Store(dc)

	dc.SetBufferedAmountLowThreshold(BufferedAmountLowThreshold)

	dc.OnOpen(func() {
		p.openOnce.Do(func() { close(p.opened) })
	})

	dc.OnMessage(func(msg pion.DataChannelMessage) {
		// recvWg gate: registers this callback as in-flight so Close
		// can wait for it before closing the inbox channel. Without
		// this, a callback that picks the inbox-send branch right as
		// Close runs would race close(p.inbox) and panic.
		p.recvWg.Add(1)
		defer p.recvWg.Done()

		// Fast-path bail if Close already fired — avoids the racy
		// send-vs-close interleaving below.
		select {
		case <-p.closed:
			return
		default:
		}

		// pion delivers fresh-allocated bytes, so we can ship them to
		// the inbox without copying.
		select {
		case p.inbox <- msg.Data:
		case <-p.closed:
		}
	})

	dc.OnBufferedAmountLow(func() {
		select {
		case p.bufferLow <- struct{}{}:
		default:
			// Already signalled; one wakeup is enough.
		}
	})

	dc.OnClose(func() {
		p.logger.Debug("webrtc: data channel closed", "peer", p.peer)
	})

	dc.OnError(func(err error) {
		p.logger.Debug("webrtc: data channel error", "peer", p.peer, "err", err)
	})
}

func (p *PeerSession) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

// convertICEServers maps the package's ICEServer view to pion's. The
// duplication is intentional: pkg/webrtc consumers should not need
// pion as a transitive import.
func convertICEServers(in []ICEServer) []pion.ICEServer {
	if len(in) == 0 {
		return nil
	}
	out := make([]pion.ICEServer, len(in))
	for i, s := range in {
		out[i] = pion.ICEServer{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: s.Credential,
		}
	}

	return out
}

func ptrOf[T any](v T) *T { return &v }

