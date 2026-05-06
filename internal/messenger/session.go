package messenger

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/network"
	"github.com/udisondev/udisend/pkg/webrtc"
)

// SignalEvent is the browser-friendly view of a signaling message that
// just crossed the Noise pipe — kind is "offer" / "answer" / "ice" /
// "bye"; payload is the raw SDP text or candidate JSON the browser
// hands to RTCPeerConnection.
type SignalEvent struct {
	Kind    string
	Payload string
}

// ErrSessionClosed signals that the session has been torn down.
var ErrSessionClosed = errors.New("messenger: session closed")

// Session is the Go-side wrapper over a network.Session. It signs
// outbound SDP/ICE with the local identity, verifies inbound signatures
// against the peer's known public identity, and decouples the
// network-level income pump from UI consumers via an inbox channel.
type Session struct {
	Peer      identity.Hash
	SessionID string

	underlying network.Session
	messenger  *Messenger
	peerPub    identity.PublicIdentity

	inbox     chan SignalEvent
	closeOnce sync.Once
	closed    chan struct{}
}

// PeerPublic exposes the peer's public identity (for the UI to display
// fingerprint, etc.).
func (s *Session) PeerPublic() identity.PublicIdentity { return s.peerPub }

// sessionIDBytes decodes the hex SessionID string into the [16]byte form
// SignedSDP needs for channel binding. The result is computed once at
// session install time, not per-Send, but the helper is small enough to
// inline at every call site instead of caching on the Session struct.
func (s *Session) sessionIDBytes() [webrtc.SessionIDSize]byte {
	var out [webrtc.SessionIDSize]byte
	raw, err := hex.DecodeString(s.SessionID)
	if err != nil {
		// Should never happen — SessionID was constructed by hex-encoding
		// the canonical [16]byte SessionID; treat as a programming bug
		// and surface as zero (Verify will then reject).
		return out
	}
	copy(out[:], raw)

	return out
}

// Send signs the event and ships it through the encrypted signaling
// pipe. Returns immediately after handing off to the transport.
//
// Channel binding: Recipient and SessionID are filled from the session's
// own state so a replay of these bytes elsewhere will fail Verify on the
// far side. IssuedAt comes from the messenger's clock so a stale capture
// fails the skew check.
func (s *Session) Send(ctx context.Context, ev SignalEvent) error {
	kind, ok := webrtc.KindFromString(ev.Kind)
	if !ok {
		return fmt.Errorf("messenger: unknown signal kind %q", ev.Kind)
	}

	signed := webrtc.SignedSDP{
		Kind:      kind,
		Recipient: s.Peer,
		SessionID: s.sessionIDBytes(),
		IssuedAt:  time.Now().UTC().Unix(),
		SDP:       ev.Payload,
	}
	signed.Sign(s.messenger.Identity())

	blob, err := signed.MarshalBinary()
	if err != nil {
		return err
	}

	return s.underlying.Send(ctx, blob)
}

// Recv blocks for the next inbound signal.
func (s *Session) Recv(ctx context.Context) (SignalEvent, error) {
	select {
	case ev := <-s.inbox:
		return ev, nil
	case <-s.closed:
		return SignalEvent{}, ErrSessionClosed
	case <-ctx.Done():
		return SignalEvent{}, ctx.Err()
	}
}

// Inbox exposes the channel for callers that prefer select{} integration.
func (s *Session) Inbox() <-chan SignalEvent { return s.inbox }

// Done returns a channel closed when the session terminates.
func (s *Session) Done() <-chan struct{} { return s.closed }

// Close shuts the session down and closes the underlying network session.
func (s *Session) Close() error {
	s.shutdown()

	return s.underlying.Close()
}

func (s *Session) shutdown() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.messenger.removeSession(s.Peer, s.SessionID)
	})
}

// handleIncomingPayload is invoked by the messenger's incomePump for
// each non-final Income event on this session. It unmarshals (which
// copies the bytes into SignedSDP fields), verifies, and pushes a
// SignalEvent into the inbox. The caller may Release the source
// buffer immediately on return.
func (s *Session) handleIncomingPayload(blob []byte) {
	var signed webrtc.SignedSDP
	if err := signed.UnmarshalBinary(blob); err != nil {
		s.messenger.logger.Warn("messenger: bad signed envelope", "peer", s.Peer, "err", err)
		return
	}

	expectedRecipient := s.messenger.Identity().Public().DestinationHash()
	expectedSession := s.sessionIDBytes()
	if err := signed.Verify(s.peerPub, expectedRecipient, expectedSession, time.Now().UTC()); err != nil {
		s.messenger.logger.Warn("messenger: signed envelope verify", "peer", s.Peer, "err", err)
		return
	}

	ev := SignalEvent{Kind: webrtc.KindString(signed.Kind), Payload: signed.SDP}
	select {
	case s.inbox <- ev:
	case <-s.closed:
		return
	}

	if signed.Kind == webrtc.SDPTypeBye {
		s.shutdown()
	}
}
