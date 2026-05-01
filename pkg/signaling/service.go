package signaling

import (
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

// Handler is invoked when a remote peer establishes a session.
type Handler func(peer identity.Hash, ch *Channel)

// Service wires Noise sessions to the DHT transport and exposes a small
// connect / accept API.
type Service struct {
	id        *identity.Identity
	transport transport.Transport
	resolver  AddressResolver
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
	Logger    *slog.Logger
}

// NewService constructs a signaling service. The caller must register
// `Service.HandlePacket` as the dht.Node's ExtraHandler.
func NewService(cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{
		id:        cfg.Identity,
		transport: cfg.Transport,
		resolver:  cfg.Resolver,
		logger:    cfg.Logger,
		sessions:  make(map[sessionKey]*Channel),
		closed:    make(chan struct{}),
	}
}

// SetHandler registers a callback invoked on every accepted incoming session.
func (s *Service) SetHandler(h Handler) {
	hp := h
	s.handler.Store(&hp)
}

// Close terminates all sessions.
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.mu.Lock()
		for _, ch := range s.sessions {
			ch.shutdown()
		}
		s.sessions = make(map[sessionKey]*Channel)
		s.mu.Unlock()
	})
}

// HandlePacket is the DHT ExtraHandler entry point.
func (s *Service) HandlePacket(ctx context.Context, pkt transport.Packet, typ byte, payload []byte) bool {
	if typ != dht.MsgRelay {
		return false
	}
	env, err := DecodeBody(payload)
	if err != nil {
		s.logger.Debug("signaling: bad envelope", "from", pkt.From, "err", err)
		return true
	}
	if env.Recipient != s.id.Public().DestinationHash() {
		// Not for us. Future: relay forward. Today: drop.
		return true
	}
	s.dispatch(ctx, pkt.From, env)
	return true
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
		if ok {
			ch.shutdown()
			s.removeSession(key)
		}
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
	m2, err := resp.WriteMessage(nil)
	if err != nil {
		s.logger.Warn("signaling: HELLO_RESP write", "err", err)
		return
	}
	ch := s.newChannel(env.Sender, env.SessionID, from, resp, false)
	s.mu.Lock()
	s.sessions[sessionKey{peer: env.Sender, sid: env.SessionID}] = ch
	s.mu.Unlock()
	if err := s.sendEnvelope(ctx, ch, InnerHelloResp, m2); err != nil {
		s.logger.Warn("signaling: send HELLO_RESP", "err", err)
		s.removeSession(sessionKey{peer: env.Sender, sid: env.SessionID})
	}
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
	ch := s.newChannel(peer, sid, addr, init, true)
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
		Sender:    s.id.Public().DestinationHash(),
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
