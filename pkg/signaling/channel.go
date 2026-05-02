package signaling

import (
	"context"
	"errors"
	"net"
	"sync"

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

	noise   *noise.Session
	inbox   chan []byte
	ready   chan struct{}

	closeOnce sync.Once
	closed    chan struct{}

	pending [][]byte // DATA frames received before handshake completes
	mu      sync.Mutex
}

func (s *Service) newChannel(peer identity.Hash, sid SessionID, remote net.Addr, ns *noise.Session, _ bool) *Channel {
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
func (c *Channel) Send(ctx context.Context, payload []byte) error {
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

// Close shuts the channel down and notifies the peer.
func (c *Channel) Close() error {
	c.closeOnce.Do(func() {
		// Best-effort BYE; we don't care about the result.
		ctx, cancel := context.WithTimeout(context.Background(), HandshakeTimeout)
		defer cancel()
		_ = c.service.sendEnvelope(ctx, c, InnerBye, nil)
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
	c.signalReady()
	c.flushPending()
}

// handleFinal drives the responder's side past handshake message 3.
func (c *Channel) handleFinal(env *Envelope) {
	if _, err := c.noise.ReadMessage(env.Payload); err != nil {
		c.service.logger.Warn("signaling: HELLO_FINAL read", "err", err)
		c.shutdown()
		return
	}
	c.signalReady()
	c.flushPending()
	if h := c.service.handler.Load(); h != nil {
		go (*h)(c.peer, c)
	}
}

// handleData decrypts a DATA frame. If the handshake hasn't yet completed
// (rare; reorder under packet-level race), buffer the ciphertext for
// later replay.
func (c *Channel) handleData(env *Envelope) {
	if !c.noise.Done() {
		c.mu.Lock()
		c.pending = append(c.pending, env.Payload)
		c.mu.Unlock()
		return
	}
	plain, err := c.noise.Decrypt(env.Payload, nil)
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
		plain, err := c.noise.Decrypt(raw, nil)
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
