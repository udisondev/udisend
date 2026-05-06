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

// noiseStaticFromIdentity bridges *identity.Identity (Suite0x01) into
// the noise.StaticKeypair shape flynn/noise needs. pkg/noise itself
// no longer imports pkg/identity (Phase 11.4 decoupling), so the
// conversion lives at the call site.
func noiseStaticFromIdentity(id *identity.Identity) noise.StaticKeypair {
	priv := id.XPriv()

	return noise.StaticKeypair{
		Private: append([]byte(nil), priv[:]...),
		Public:  id.AgreementPublic(),
	}
}

// AddressResolver returns the network address for a destination hash.
// The signaling service uses presence.Resolver in production but tests
// can substitute a static map. peer is identity.PeerID — Hash satisfies
// the interface, custom suite-aware peer types also work.
type AddressResolver interface {
	Lookup(ctx context.Context, peer identity.PeerID) (*presence.Record, error)
}

// Router is the subset of routing-table behaviour the signaling Service
// needs to forward envelopes whose recipient is not us.
//
// NextHop is the best-effort path used by OUR outbound Connect calls —
// it may issue iterative DHT lookups to discover the route. We trust
// our own intent, so the amplification is acceptable.
//
// LocalNextHop is the cache-only path used when forwarding envelopes
// originated by a remote peer (relay). A cache miss here MUST drop
// rather than trigger iterative-find — otherwise a hostile peer could
// send a stream of envelopes for unknown recipients and burn our
// outbound bandwidth (Phase 9 audit: relay → iterative-lookup
// amplification, ~24 outbound DHT FIND_NODEs per inbound envelope).
type Router interface {
	NextHop(ctx context.Context, target identity.Hash) (net.Addr, bool)
	LocalNextHop(target identity.Hash) (net.Addr, bool)
}

// Handler is invoked when a remote peer establishes a session.
type Handler func(peer identity.Hash, ch *Channel)

// MeshHandler is invoked when a peer sends an InnerMesh* envelope
// (offer / answer / candidate) on an established channel. The payload
// is already decrypted under the channel's Noise key. The handler
// runs on the dispatcher goroutine — implementations MUST not block.
//
// Channels are not modified before the handler fires, so the same
// *Channel handle the application receives via Handler is what the
// mesh-handler sees here, allowing the network-layer mesh-signaler
// to call ch.SendMesh on the responder side.
type MeshHandler func(ch *Channel, kind byte, sdp []byte)

// Service wires Noise sessions to the DHT transport and exposes a small
// connect / accept API.
// MaxHalfOpenPerIP caps the number of in-flight (handshake-not-yet-
// complete) responder sessions a single source IP may hold. Protects
// against a HELLO_INIT flood where each accepted INIT pins a Noise
// responder allocation for the full HandshakeTimeout (6s) — without
// the cap, an attacker rotating SessionIDs from one IP can keep
// thousands of responder allocations live concurrently. The cap
// matches what a real client could ever need (one or two retries,
// not eight).
const MaxHalfOpenPerIP = 8

type Service struct {
	id        *identity.Identity
	selfDH    identity.Hash // cached id.Public().DestinationHash() — used per packet
	transport transport.Transport
	resolver  AddressResolver
	router    atomic.Pointer[Router]
	logger    *slog.Logger

	mu       sync.Mutex
	sessions map[sessionKey]*Channel
	// halfOpenByIP is the per-IP counter of accepted-but-incomplete
	// responder handshakes. Incremented in acceptInit before allocation,
	// decremented when the channel either completes the handshake or is
	// removed (timeout / explicit shutdown). Guarded by s.mu — same
	// invariant as `sessions`.
	halfOpenByIP map[string]int

	handler     atomic.Pointer[Handler]
	meshHandler atomic.Pointer[MeshHandler]

	relayMu   sync.Mutex
	relayHits map[string][]time.Time

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
		id:           cfg.Identity,
		selfDH:       cfg.Identity.Public().DestinationHash(),
		transport:    cfg.Transport,
		resolver:     cfg.Resolver,
		logger:       cfg.Logger,
		sessions:     make(map[sessionKey]*Channel),
		halfOpenByIP: make(map[string]int),
		closed:       make(chan struct{}),
	}
	if cfg.Router != nil {
		r := cfg.Router
		s.router.Store(&r)
	}
	return s
}

// SetMeshHandler registers a callback invoked when an InnerMesh*
// envelope arrives on an established channel. Pass nil to disable.
func (s *Service) SetMeshHandler(h MeshHandler) {
	if h == nil {
		s.meshHandler.Store(nil)

		return
	}
	hp := h
	s.meshHandler.Store(&hp)
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
		s.relay(ctx, pkt.From, env)
		return true
	}
	s.dispatch(ctx, pkt.From, env)
	return true
}

// relayBudgetPerMinute caps how many envelopes a single source IP may
// have us forward per minute. Drops over the cap. Stops a hostile peer
// from making us its outbound bandwidth amplifier.
const relayBudgetPerMinute = 60

// relay forwards an envelope whose recipient is not us. Phase 9 audit:
//
//   - Drops on cache-miss (no iterative DHT lookup) — relayed envelopes
//     for unknown recipients cannot drive us into ~24 outbound
//     FIND_NODEs each.
//   - Per-source-IP token bucket limits how often a single peer can
//     have us forward.
//   - Hop-count enforcement (was already present) caps trip length.
func (s *Service) relay(ctx context.Context, from net.Addr, env *Envelope) {
	rp := s.router.Load()
	if rp == nil {
		return
	}
	if env.Hops >= MaxHops {
		s.logger.Debug("signaling: relay drop (hop limit)",
			"recipient", env.Recipient, "hops", env.Hops)
		return
	}
	if !s.allowRelayFrom(from) {
		s.logger.Debug("signaling: relay drop (rate-limited)",
			"from", from, "recipient", env.Recipient)
		return
	}
	next, ok := (*rp).LocalNextHop(env.Recipient)
	if !ok {
		s.logger.Debug("signaling: relay drop (no cached route)",
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

// relaySweepThreshold is the map size at which allowRelayFrom does a
// full GC pass. Below it we only trim the per-IP list of the IP we
// just observed — O(1) amortised. Above it we sweep the full table
// once and reset the counter. Bounds the worst-case-per-call cost
// flagged in the Phase 9 review.
const relaySweepThreshold = 256

// allowRelayFrom is the per-source-IP gate. Sliding-window counter on
// `from`'s IP; drop when over the cap.
func (s *Service) allowRelayFrom(from net.Addr) bool {
	host := relayHostKey(from)

	now := time.Now()
	cutoff := now.Add(-time.Minute)

	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	if s.relayHits == nil {
		s.relayHits = make(map[string][]time.Time)
	}

	// Trim only the per-IP list we are about to update — O(window) per
	// call. Full-map sweep happens only when the table grows past
	// threshold, which keeps memory bounded under attack without
	// burning CPU on every legitimate relay.
	hits := trimRelayTimes(s.relayHits[host], cutoff)
	if len(s.relayHits) > relaySweepThreshold {
		for ip, ts := range s.relayHits {
			if ip == host {
				continue
			}
			trimmed := trimRelayTimes(ts, cutoff)
			if len(trimmed) == 0 {
				delete(s.relayHits, ip)
			} else {
				s.relayHits[ip] = trimmed
			}
		}
	}
	if len(hits) >= relayBudgetPerMinute {
		s.relayHits[host] = hits

		return false
	}
	s.relayHits[host] = append(hits, now)

	return true
}

// relayHostKey extracts a stable key for rate-limiting from a remote
// address. *net.UDPAddr — preferred, drops the port and the IPv6 zone
// identifier so an attacker can't pivot the bucket by varying %eth0.
// Falls back to the raw String() for non-UDP transports (in-memory
// pipe used by some tests).
func relayHostKey(addr net.Addr) string {
	if u, ok := addr.(*net.UDPAddr); ok && u.IP != nil {
		return u.IP.String()
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}

	return host
}

func trimRelayTimes(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	if i == 0 {
		return ts
	}

	return ts[i:]
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
	case InnerMeshOffer, InnerMeshAnswer, InnerMeshCandidate:
		if !ok {
			s.logger.Debug("signaling: mesh envelope without session", "type", env.InnerType)
			return
		}
		ch.handleMesh(env)
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
		ch.recvMu.Lock()
		_, err := ch.noise.Decrypt(env.Payload, nil)
		ch.recvMu.Unlock()
		if err != nil {
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
	// Per-IP half-open cap: refuse the INIT outright if this source IP
	// already has MaxHalfOpenPerIP responder slots in flight. Without
	// this gate, an attacker rotating SessionIDs from one IP can pin
	// HandshakeTimeout × N responder allocations live concurrently.
	//
	// Reserve the slot under the first lock so that the expensive
	// noise.NewResponder + ReadMessage + WriteMessage path runs only for
	// callers that already hold a reservation. Without this ordering, a
	// burst of N concurrent INITs from one IP all pass an unlocked
	// pre-check, trigger N Curve25519 DH operations, and only then drop
	// to the cap — multiplying CPU work per inbound packet far above 1×.
	ipKey := relayHostKey(from)
	s.mu.Lock()
	if ipKey != "" {
		if s.halfOpenByIP[ipKey] >= MaxHalfOpenPerIP {
			s.mu.Unlock()
			s.logger.Debug("signaling: HELLO_INIT drop (half-open cap)",
				"from", from, "in_flight", MaxHalfOpenPerIP)

			return
		}
		s.halfOpenByIP[ipKey]++
	}
	s.mu.Unlock()

	// On any error path before s.sessions[key] is set we MUST release the
	// reservation; otherwise a burst of malformed INITs leaks slots until
	// the per-IP cap is permanently saturated.
	committed := false
	defer func() {
		if committed || ipKey == "" {
			return
		}
		s.mu.Lock()
		if n := s.halfOpenByIP[ipKey]; n > 1 {
			s.halfOpenByIP[ipKey] = n - 1
		} else {
			delete(s.halfOpenByIP, ipKey)
		}
		s.mu.Unlock()
	}()

	resp, err := noise.NewResponder(noiseStaticFromIdentity(s.id))
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
	ch.halfOpenIP = ipKey
	s.mu.Lock()
	s.sessions[key] = ch
	s.mu.Unlock()
	committed = true

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
// destination hash via the resolver. Returns true ONLY when the
// lookup succeeds AND the keys match. A budget-exhausted lookup is a
// hard reject — the caller (Channel.verifyAndAdmit) shuts the
// channel down before any DATA frame leaks to the application.
//
// Runs OFF the dispatcher goroutine so a slow resolver does not
// block DHT/signaling packet processing. The 4-second budget is
// generous because cold-cache first-contact may need the publisher's
// PutValue to have replicated; if it hasn't by then, the connection
// is refused and the caller is expected to retry.
func (s *Service) verifySenderIdentity(parent context.Context, sender identity.Hash, sess *noise.Session) bool {
	got, err := sess.PeerStatic()
	if err != nil {
		return false
	}

	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()

	backoff := 200 * time.Millisecond
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	for ctx.Err() == nil {
		lookupCtx, lcancel := context.WithTimeout(ctx, time.Second)
		rec, err := s.resolver.Lookup(lookupCtx, sender)
		lcancel()
		if err == nil && rec != nil {
			match := bytes.Equal(got, rec.Public.XPub[:])
			if !match {
				s.logger.Warn("signaling: noise static does not match presence record",
					"sender", sender)
			}

			return match
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
		}
		if backoff < time.Second {
			backoff *= 2
		}
		timer.Reset(backoff)
	}

	return false
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
	init, err := noise.NewInitiator(noiseStaticFromIdentity(s.id), rec.Public.AgreementPublic())
	if err != nil {
		return nil, fmt.Errorf("signaling: noise init: %w", err)
	}
	sid, err := NewSessionID()
	if err != nil {
		return nil, fmt.Errorf("signaling: new session id: %w", err)
	}
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

	// time.NewTimer + Stop instead of time.After: Connect runs once per
	// session attempt, but on graceful shutdown many concurrent Connects
	// can be cancelled mid-handshake. NewTimer + Stop releases the timer
	// slot immediately on the ctx-cancel / closed paths; time.After would
	// hold it until natural expiry (HandshakeTimeout).
	timer := time.NewTimer(HandshakeTimeout)
	defer timer.Stop()
	select {
	case <-ch.ready:
		return ch, nil
	case <-timer.C:
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
		s.releaseHalfOpenLocked(ch)
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
	if ch, ok := s.sessions[key]; ok {
		delete(s.sessions, key)
		s.releaseHalfOpenLocked(ch)
	}
	s.mu.Unlock()
}

// releaseHalfOpenLocked decrements the per-IP half-open counter for ch
// IFF the channel was registered as a responder slot AND has not yet
// been settled (i.e. neither handshake-completion nor a previous
// removal already accounted for it). Caller MUST hold s.mu.
func (s *Service) releaseHalfOpenLocked(ch *Channel) {
	if ch == nil || ch.halfOpenIP == "" {
		return
	}
	if !ch.halfOpenSettled.CompareAndSwap(false, true) {
		return
	}
	if n := s.halfOpenByIP[ch.halfOpenIP]; n > 1 {
		s.halfOpenByIP[ch.halfOpenIP] = n - 1
	} else {
		delete(s.halfOpenByIP, ch.halfOpenIP)
	}
}

// settleHalfOpen is the public counterpart to releaseHalfOpenLocked
// invoked from Channel.handleFinal once the responder side completes
// the handshake — at that point the slot is no longer "half-open" and
// the counter must be decremented even though the session itself stays
// alive.
func (s *Service) settleHalfOpen(ch *Channel) {
	if ch == nil || ch.halfOpenIP == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseHalfOpenLocked(ch)
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
