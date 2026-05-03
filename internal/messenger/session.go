package messenger

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
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

// Session is the Go-side wrapper over a signaling.Channel exposed to the
// HTTP/WS layer. It signs outbound SDP/ICE with the local identity,
// verifies inbound signatures against the peer's known public identity,
// and decouples the transport pump from the consumer via an inbox channel.
type Session struct {
	Peer      identity.Hash
	SessionID string

	channel   *signaling.Channel
	messenger *Messenger
	peerPub   identity.PublicIdentity

	inbox     chan SignalEvent
	closeOnce sync.Once
	closed    chan struct{}
}

// PeerPublic exposes the peer's public identity (for the UI to display
// fingerprint, etc.).
func (s *Session) PeerPublic() identity.PublicIdentity { return s.peerPub }

// Send signs the event and ships it through the encrypted signaling pipe.
// Returns immediately after handing off to the transport.
func (s *Session) Send(ctx context.Context, ev SignalEvent) error {
	kind, ok := webrtc.KindFromString(ev.Kind)
	if !ok {
		return fmt.Errorf("messenger: unknown signal kind %q", ev.Kind)
	}
	signed := webrtc.SignedSDP{Kind: kind, SDP: ev.Payload}
	signed.Sign(s.messenger.id)
	blob, err := signed.MarshalBinary()
	if err != nil {
		return err
	}
	return s.channel.Send(ctx, blob)
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

// Close shuts the session down and closes the underlying signaling channel.
func (s *Session) Close() error {
	s.shutdown()
	return s.channel.Close()
}

func (s *Session) shutdown() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.messenger.removeSession(s.Peer, s.SessionID)
	})
}

// recvLoop pumps signaling.Channel into the session inbox until either
// closes. Runs as a goroutine.
func (s *Session) recvLoop() {
	// Recv blocks indefinitely; the loop exits when channel.Close fires
	// or the peer sends BYE — neither path needs a context to cancel.
	ctx := context.Background()
	for {
		blob, err := s.channel.Recv(ctx)
		if err != nil {
			s.shutdown()
			return
		}
		var signed webrtc.SignedSDP
		if err := signed.UnmarshalBinary(blob); err != nil {
			s.messenger.cfg.Logger.Warn("messenger: bad signed envelope", "peer", s.Peer, "err", err)
			continue
		}
		if err := signed.Verify(s.peerPub); err != nil {
			s.messenger.cfg.Logger.Warn("messenger: signed envelope verify", "peer", s.Peer, "err", err)
			continue
		}
		ev := SignalEvent{Kind: webrtc.KindString(signed.Kind), Payload: signed.SDP}
		select {
		case s.inbox <- ev:
		case <-s.closed:
			return
		}
		if signed.Kind == webrtc.SDPTypeBye {
			s.shutdown()
			return
		}
	}
}
