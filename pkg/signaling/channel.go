package signaling

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/noise"
)

// inboxSize bounds how many DATA messages can buffer before the reader
// must drain. 256 is generous for signaling traffic — if a channel is
// dropping, slowing the writer is preferable to unbounded growth.
const inboxSize = 256

// ErrChannelClosed signals that the channel has been shut down.
var ErrChannelClosed = errors.New("signaling: channel closed")

// Channel is a bidirectional encrypted byte-message stream between two
// peers. Reads return whole messages; the channel does not provide a
// stream abstraction.
type Channel struct {
	peer    identity.Hash
	sid     SessionID
	remote  net.Addr
	service *Service

	// sendMu serialises Channel.Send: flynn/noise.CipherState advances
	// the AEAD nonce on each Encrypt and is NOT goroutine-safe; concurrent
	// Encrypt calls would race the counter and re-use a nonce, breaking
	// confidentiality and authenticity. The HTTP+SSE bridge fans out
	// /api/signal/send requests across goroutines so this is reachable.
	sendMu sync.Mutex
	// recvMu serialises Decrypt across the dispatcher's handleData path
	// AND verifyAndAdmit's flushPending. The receive-side CipherState
	// has its own counter that the same race-condition concerns apply
	// to as sendMu's send-side state.
	recvMu sync.Mutex
	noise  *noise.Session
	inbox  chan []byte
	ready  chan struct{}

	// verified is true once the responder's identity check (peer-static
	// vs presence record) has succeeded. Frames received in the
	// post-handshake-pre-verification window are buffered in `pending`
	// and only released to inbox after this flips. Closes the silent-
	// admission window flagged in the Phase 9 review.
	verified atomic.Bool

	closeOnce sync.Once
	closed    chan struct{}

	pending [][]byte // DATA frames received before handshake+verify completes
	mu      sync.Mutex

	// handshakeTimer fires HandshakeTimeout after acceptInit if the
	// initiator never completes the handshake (responder DoS guard).
	// nil for initiator-side channels. Stopped once handleFinal lands
	// so a successful handshake doesn't leave a closure on the runtime
	// timer wheel.
	handshakeTimer *time.Timer
}

func (s *Service) newChannel(peer identity.Hash, sid SessionID, remote net.Addr, ns *noise.Session) *Channel {
	return &Channel{
		peer:    peer,
		sid:     sid,
		remote:  remote,
		service: s,
		noise:   ns,
		inbox:   make(chan []byte, inboxSize),
		ready:   make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

// Peer returns the remote peer's destination hash.
func (c *Channel) Peer() identity.Hash { return c.peer }

// SessionID returns the session identifier shared by both ends of the channel.
func (c *Channel) SessionID() SessionID { return c.sid }

// Send encrypts payload and ships it. The payload is one logical message;
// peers see it as a single Recv() call.
//
// Encrypt + send are serialised via sendMu so the AEAD nonce counter
// inside the noise CipherState is never advanced concurrently. Holding
// the lock across the transport.Send is fine: signaling traffic is low
// volume (SDP + ICE + small app messages), and out-of-order delivery
// would already break the receiver's nonce sequence anyway.
func (c *Channel) Send(ctx context.Context, payload []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if !c.noise.Done() {
		return errors.New("signaling: handshake not complete")
	}
	ct, err := c.noise.Encrypt(payload, nil)
	if err != nil {
		return err
	}

	return c.service.sendEnvelope(ctx, c, InnerData, ct)
}

// Recv blocks until a DATA frame arrives or the channel closes.
func (c *Channel) Recv(ctx context.Context) ([]byte, error) {
	select {
	case msg := <-c.inbox:
		return msg, nil
	case <-c.closed:
		return nil, ErrChannelClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close shuts the channel down and notifies the peer with an
// authenticated BYE: an empty payload encrypted under the same noise
// key used for DATA. A relay watching the wire cannot forge a BYE
// because the AEAD nonce + key are private to the two endpoints.
//
// Encrypt + sendEnvelope run under the same sendMu used by Send so the
// BYE's nonce is contiguous with any concurrent DATA frame — otherwise
// a Send winning the lock between Encrypt and sendEnvelope would
// produce a higher-nonce DATA delivered before our BYE, the receiver
// would advance past our BYE's nonce, and the BYE would be rejected
// as a replay. Best-effort: failure to send does not block teardown.
func (c *Channel) Close() error {
	c.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()

		c.sendMu.Lock()
		if c.noise.Done() {
			if ct, err := c.noise.Encrypt(nil, nil); err == nil {
				_ = c.service.sendEnvelope(ctx, c, InnerBye, ct)
			}
		}
		c.sendMu.Unlock()

		c.service.unregister(sessionKey{peer: c.peer, sid: c.sid})
		close(c.closed)
	})

	return nil
}

// shutdown closes the channel without sending a BYE — used by Service when
// the session is being torn down externally.
func (c *Channel) shutdown() {
	c.closeOnce.Do(func() {
		close(c.closed)
	})
}

// handleResp drives the initiator's side past handshake message 2.
// Initiator-side channels are inherently verified: Service.Connect
// calls resolver.Lookup before initiating the handshake, so the
// X25519 static the responder authenticates with is already known to
// match the destination_hash we connected to. Mark verified here so
// flushPending unblocks any DATA frames that raced ahead.
func (c *Channel) handleResp(ctx context.Context, env *Envelope) {
	if _, err := c.noise.ReadMessage(env.Payload); err != nil {
		c.service.logger.Warn("signaling: HELLO_RESP read", "err", err)
		c.shutdown()
		return
	}
	m3, err := c.noise.WriteMessage(nil)
	if err != nil {
		c.service.logger.Warn("signaling: HELLO_FINAL write", "err", err)
		c.shutdown()
		return
	}
	if err := c.service.sendEnvelope(ctx, c, InnerHelloFinal, m3); err != nil {
		c.service.logger.Warn("signaling: send HELLO_FINAL", "err", err)
		c.shutdown()
		return
	}
	c.verified.Store(true)
	c.signalReady()
	c.flushPending()
}

// handleFinal drives the responder's side past handshake message 3.
// Noise XK pattern carries the initiator's static key in m3, so this
// is the first point at which resp.PeerStatic() is actually defined.
//
// Identity verification (peer-static vs presence record) is dispatched
// to a goroutine so the dispatcher's recv path is never blocked by a
// resolver round-trip. Until verification succeeds, the channel is
// installed but no DATA frames reach the application: handleData
// buffers them in `pending` (capped) and flushPending releases them
// only after `verified` flips true. Mismatch / lookup-budget
// exhaustion -> the channel is shut down before any frame leaks. This
// closes the recv-goroutine block AND the silent-admission window
// flagged in the Phase 9 review.
func (c *Channel) handleFinal(env *Envelope) {
	if _, err := c.noise.ReadMessage(env.Payload); err != nil {
		c.service.logger.Warn("signaling: HELLO_FINAL read", "err", err)
		c.shutdown()
		return
	}

	if c.handshakeTimer != nil {
		c.handshakeTimer.Stop()
	}

	go c.verifyAndAdmit()
}

// verifyAndAdmit runs the resolver-backed identity check off the
// dispatcher goroutine. On match: the channel becomes usable
// (signalReady + flushPending + handler invoked). On mismatch /
// resolver budget exhaustion: the channel is removed and shut down
// before any DATA frame is decoded into the application inbox.
func (c *Channel) verifyAndAdmit() {
	if !c.service.verifySenderIdentity(context.Background(), c.peer, c.noise) {
		c.service.logger.Warn("signaling: HELLO_FINAL identity mismatch", "claimed", c.peer)
		c.service.removeSession(sessionKey{peer: c.peer, sid: c.sid})
		c.shutdown()
		return
	}
	c.verified.Store(true)
	c.signalReady()
	c.flushPending()

	if h := c.service.handler.Load(); h != nil {
		go (*h)(c.peer, c)
	}
}

// MaxPendingDataFrames bounds how many DATA frames a channel will
// buffer while the handshake is still in flight. Real reordering on a
// single UDP socket is bounded; the cap is there so an attacker who
// floods DATA before HELLO_FINAL cannot grow the per-channel slice
// without limit (design.md §8 DoS).
const MaxPendingDataFrames = 8

// handleData decrypts a DATA frame. If either the handshake has not
// yet completed OR the responder-side identity verification is still
// in flight, buffer the ciphertext until verifyAndAdmit flushes it.
// Bounded by MaxPendingDataFrames so a peer that finishes handshake
// then floods DATA before identity check completes cannot grow the
// per-channel slice without limit.
func (c *Channel) handleData(env *Envelope) {
	if !c.noise.Done() || !c.verified.Load() {
		c.mu.Lock()
		if len(c.pending) < MaxPendingDataFrames {
			c.pending = append(c.pending, env.Payload)
		}
		c.mu.Unlock()
		return
	}

	c.recvMu.Lock()
	plain, err := c.noise.Decrypt(env.Payload, nil)
	c.recvMu.Unlock()
	if err != nil {
		c.service.logger.Warn("signaling: data decrypt", "err", err)
		return
	}
	select {
	case c.inbox <- plain:
	case <-c.closed:
	}
}

func (c *Channel) flushPending() {
	c.mu.Lock()
	frames := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, raw := range frames {
		if !c.noise.Done() {
			return
		}
		c.recvMu.Lock()
		plain, err := c.noise.Decrypt(raw, nil)
		c.recvMu.Unlock()
		if err != nil {
			c.service.logger.Warn("signaling: pending decrypt", "err", err)
			continue
		}
		select {
		case c.inbox <- plain:
		case <-c.closed:
			return
		}
	}
}

func (c *Channel) signalReady() {
	select {
	case <-c.ready:
	default:
		close(c.ready)
	}
}
