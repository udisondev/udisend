package signaling

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/noise"
	"github.com/udisondev/udisend/pkg/presence"
	"github.com/udisondev/udisend/pkg/transport"
)

// HandshakeTimeout caps how long a Connect call waits for the handshake
// to complete.
const HandshakeTimeout = 6 * time.Second

// AddressResolver returns the network address for a destination hash.
// The signaling service uses presence.Resolver in production but tests
// can substitute a static map.
type AddressResolver interface {
	Lookup(ctx context.Context, peer identity.Hash) (*presence.Record, error)
}

// Router is the subset of routing-table behaviour the signaling Service
// needs to forward envelopes whose recipient is not us. NextHop returns
// the next-hop address closest to `target` and ok=true, or ok=false if
// the local node knows no route. Implementations must be safe for
// concurrent use and may issue bounded DHT path-requests inside (the
// passed ctx caps the round trip).
type Router interface {
	NextHop(ctx context.Context, target identity.Hash) (net.Addr, bool)
}

// Handler is invoked when a remote peer establishes a session.
type Handler func(peer identity.Hash, ch *Channel)

// Service wires Noise sessions to the DHT transport and exposes a small
// connect / accept API.
type Service struct {
	id        *identity.Identity
	selfDH    identity.Hash // cached id.Public().DestinationHash() — used per packet
	transport transport.Transport
	resolver  AddressResolver
	router    atomic.Pointer[Router]
	logger    *slog.Logger

	mu       sync.Mutex
	sessions map[sessionKey]*Channel

	handler atomic.Pointer[Handler]

	closeOnce sync.Once
	closed    chan struct{}
}

type sessionKey struct {
	peer identity.Hash
	sid  SessionID
}

// Config configures NewService.
type Config struct {
	Identity  *identity.Identity
	Transport transport.Transport
	Resolver  AddressResolver
	// Router, if non-nil, enables hop-by-hop forwarding of envelopes whose
	// recipient is not us. Without it, foreign envelopes are dropped.
	Router Router
	Logger *slog.Logger
}

// NewService constructs a signaling service. The caller must register
// `Service.HandlePacket` as the dht.Node's ExtraHandler.
func NewService(cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Service{
		id:        cfg.Identity,
		selfDH:    cfg.Identity.Public().DestinationHash(),
		transport: cfg.Transport,
		resolver:  cfg.Resolver,
		logger:    cfg.Logger,
		sessions:  make(map[sessionKey]*Channel),
		closed:    make(chan struct{}),
	}
	if cfg.Router != nil {
		r := cfg.Router
		s.router.Store(&r)
	}
	return s
}

// SetHandler registers a callback invoked on every accepted incoming session.
func (s *Service) SetHandler(h Handler) {
	hp := h
	s.handler.Store(&hp)
}

// SetRouter wires (or replaces) the routing-table backend used by relay
// forwarding. Pass nil to disable forwarding (envelopes whose recipient
// is not us will then be dropped silently).
func (s *Service) SetRouter(r Router) {
	if r == nil {
		s.router.Store(nil)
		return
	}
	s.router.Store(&r)
}

// Close terminates all sessions.
//
// Snapshot the active channels under s.mu, then drop the lock before
// invoking ch.shutdown() — Channel.Close (which Session.Close in turn
// triggers from a different goroutine) re-acquires s.mu via
// service.unregister inside its own closeOnce.Do body. Holding s.mu
// across the shutdown loop while the shutdown closure is queued behind
// us creates an AB/BA cycle (Service.Close holds s.mu waits for
// closeOnce; Channel.Close holds closeOnce waits for s.mu). Same
// pattern as Messenger.Close documents.
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)

		s.mu.Lock()
		toShutdown := make([]*Channel, 0, len(s.sessions))
		for _, ch := range s.sessions {
			toShutdown = append(toShutdown, ch)
		}
		s.sessions = make(map[sessionKey]*Channel)
		s.mu.Unlock()

		for _, ch := range toShutdown {
			ch.shutdown()
		}
	})
}

// HandlePacket is the DHT ExtraHandler entry point.
func (s *Service) HandlePacket(ctx context.Context, pkt transport.Packet, typ byte, payload []byte) bool {
	if typ != dht.MsgRelay {
		return false
	}
	select {
	case <-s.closed:
		return true // service shut down — don't insert into a cleared map
	default:
	}
	env, err := DecodeBody(payload)
	if err != nil {
		s.logger.Debug("signaling: bad envelope", "from", pkt.From, "err", err)
		return true
	}
	if env.Recipient != s.selfDH {
		s.relay(ctx, env)
		return true
	}
	s.dispatch(ctx, pkt.From, env)
	return true
}

// relay forwards an envelope whose recipient is not us. design.md §4
// (hop-by-hop signaling routing). Drops with a warning if Hops is at
// MaxHops, if no Router is configured, or if no next hop is known.
func (s *Service) relay(ctx context.Context, env *Envelope) {
	rp := s.router.Load()
	if rp == nil {
		return
	}
	if env.Hops >= MaxHops {
		s.logger.Debug("signaling: relay drop (hop limit)",
			"recipient", env.Recipient, "hops", env.Hops)
		return
	}
	next, ok := (*rp).NextHop(ctx, env.Recipient)
	if !ok {
		s.logger.Debug("signaling: relay drop (no route)",
			"recipient", env.Recipient)
		return
	}
	env.Hops++
	blob, err := env.Encode()
	if err != nil {
		s.logger.Warn("signaling: relay encode", "err", err)
		return
	}
	if err := s.transport.Send(ctx, next, blob); err != nil {
		s.logger.Warn("signaling: relay send",
			"recipient", env.Recipient, "next", next, "err", err)
	}
}

func (s *Service) dispatch(ctx context.Context, from net.Addr, env *Envelope) {
	key := sessionKey{peer: env.Sender, sid: env.SessionID}
	s.mu.Lock()
	ch, ok := s.sessions[key]
	s.mu.Unlock()

	switch env.InnerType {
	case InnerHelloInit:
		if ok {
			s.logger.Debug("signaling: duplicate HELLO_INIT", "peer", env.Sender)
			return
		}
		s.acceptInit(ctx, from, env)
	case InnerHelloResp:
		if !ok {
			s.logger.Debug("signaling: HELLO_RESP without session")
			return
		}
		ch.handleResp(ctx, env)
	case InnerHelloFinal:
		if !ok {
			s.logger.Debug("signaling: HELLO_FINAL without session")
			return
		}
		ch.handleFinal(env)
	case InnerData:
		if !ok {
			s.logger.Debug("signaling: DATA without session")
			return
		}
		ch.handleData(env)
	case InnerBye:
		if !ok {
			return
		}
		// Authenticated BYE: payload is an empty plaintext encrypted
		// under the same noise key. A relay forging a BYE cannot
		// supply a valid AEAD tag for the next nonce, so Decrypt
		// fails and we keep the session up.
		if !ch.noise.Done() {
			s.logger.Debug("signaling: BYE before handshake done", "peer", env.Sender)
			return
		}
		if _, err := ch.noise.Decrypt(env.Payload, nil); err != nil {
			s.logger.Debug("signaling: forged BYE drop", "peer", env.Sender, "err", err)
			return
		}
		ch.shutdown()
		s.removeSession(key)
	default:
		s.logger.Debug("signaling: unknown inner type", "type", env.InnerType)
	}
}

func (s *Service) acceptInit(ctx context.Context, from net.Addr, env *Envelope) {
	resp, err := noise.NewResponder(s.id)
	if err != nil {
		s.logger.Warn("signaling: responder init", "err", err)
		return
	}
	if _, err := resp.ReadMessage(env.Payload); err != nil {
		s.logger.Warn("signaling: HELLO_INIT decrypt", "err", err)
		return
	}

	// Note: Noise XK pattern (e,es / e,ee / s,se) does NOT carry the
	// initiator's static key in m1 — it arrives in m3 (HELLO_FINAL).
	// Identity binding therefore happens in Channel.handleFinal once
	// resp.PeerStatic() actually reflects the initiator's authenticated
	// static key.

	m2, err := resp.WriteMessage(nil)
	if err != nil {
		s.logger.Warn("signaling: HELLO_RESP write", "err", err)
		return
	}

	key := sessionKey{peer: env.Sender, sid: env.SessionID}
	ch := s.newChannel(env.Sender, env.SessionID, from, resp)
	s.mu.Lock()
	s.sessions[key] = ch
	s.mu.Unlock()

	// Half-open responder DoS guard (design.md §8): if the initiator
	// never sends HELLO_FINAL the channel sits forever consuming memory
	// (pending buffer, slot in s.sessions). Schedule eviction after the
	// same window Connect honours; a real peer always finishes within it.
	// Stored on the Channel so handleFinal can Stop the timer once the
	// handshake completes successfully.
	ch.handshakeTimer = time.AfterFunc(HandshakeTimeout, func() {
		if ch.noise.Done() {
			return
		}

		s.removeSession(key)
	})

	if err := s.sendEnvelope(ctx, ch, InnerHelloResp, m2); err != nil {
		s.logger.Warn("signaling: send HELLO_RESP", "err", err)
		s.removeSession(key)
	}
}

// verifySenderIdentity checks that the noise session's authenticated
// peer-static (X25519) matches the one bound to env.Sender's
// destination hash via the resolver. Looks up with a short timeout
// (1s) so a slow / unanswered DHT query never blocks the dispatcher
// goroutine; if the resolver does not return a record promptly the
// binding is deferred to the SignedSDP layer — Session.recvLoop
// refuses any payload whose Ed25519 signature does not match the
// contact's pubkey.
func (s *Service) verifySenderIdentity(parent context.Context, sender identity.Hash, sess *noise.Session) bool {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()

	rec, err := s.resolver.Lookup(ctx, sender)
	if err != nil || rec == nil {
		return true // unknown / slow: defer to higher layer
	}

	got, err := sess.PeerStatic()
	if err != nil {
		return false
	}

	return bytes.Equal(got, rec.Public.XPub[:])
}

// Connect initiates a handshake with `peer` and returns the open channel.
func (s *Service) Connect(ctx context.Context, peer identity.Hash) (*Channel, error) {
	rec, err := s.resolver.Lookup(ctx, peer)
	if err != nil {
		return nil, fmt.Errorf("signaling: resolve peer: %w", err)
	}
	addr, err := s.transport.Dial(rec.Address)
	if err != nil {
		return nil, fmt.Errorf("signaling: dial %q: %w", rec.Address, err)
	}
	init, err := noise.NewInitiator(s.id, rec.Public)
	if err != nil {
		return nil, fmt.Errorf("signaling: noise init: %w", err)
	}
	sid := NewSessionID()
	ch := s.newChannel(peer, sid, addr, init)
	s.mu.Lock()
	s.sessions[sessionKey{peer: peer, sid: sid}] = ch
	s.mu.Unlock()

	m1, err := init.WriteMessage(nil)
	if err != nil {
		s.removeSession(sessionKey{peer: peer, sid: sid})
		return nil, fmt.Errorf("signaling: HELLO_INIT: %w", err)
	}
	if err := s.sendEnvelope(ctx, ch, InnerHelloInit, m1); err != nil {
		s.removeSession(sessionKey{peer: peer, sid: sid})
		return nil, fmt.Errorf("signaling: send HELLO_INIT: %w", err)
	}

	select {
	case <-ch.ready:
		return ch, nil
	case <-time.After(HandshakeTimeout):
		s.removeSession(sessionKey{peer: peer, sid: sid})
		return nil, errors.New("signaling: handshake timeout")
	case <-ctx.Done():
		s.removeSession(sessionKey{peer: peer, sid: sid})
		return nil, ctx.Err()
	case <-s.closed:
		return nil, errors.New("signaling: service closed")
	}
}

// removeSession unregisters and shuts down the channel.
func (s *Service) removeSession(key sessionKey) {
	s.mu.Lock()
	ch, ok := s.sessions[key]
	if ok {
		delete(s.sessions, key)
	}
	s.mu.Unlock()
	if ok {
		ch.shutdown()
	}
}

// unregister removes the session from the map without shutting it down —
// used by Channel.Close which already owns the shutdown sequence.
func (s *Service) unregister(key sessionKey) {
	s.mu.Lock()
	delete(s.sessions, key)
	s.mu.Unlock()
}

// sendEnvelope is the single outbound funnel — every signaling packet
// goes through here so we can later add rate-limiting / batching.
func (s *Service) sendEnvelope(ctx context.Context, ch *Channel, innerType byte, payload []byte) error {
	env := &Envelope{
		Recipient: ch.peer,
		Sender:    s.selfDH,
		SessionID: ch.sid,
		InnerType: innerType,
		Payload:   payload,
	}
	blob, err := env.Encode()
	if err != nil {
		return err
	}
	return s.transport.Send(ctx, ch.remote, blob)
}
