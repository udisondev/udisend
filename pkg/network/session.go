package network

import (
	"context"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
)

// SessionID is the 16-byte handshake-derived identifier shared by both
// ends of a signaling session. The alias keeps callers from importing
// pkg/signaling directly.
type SessionID = signaling.SessionID

// Session is a value-type handle to an active session. Send and Close
// are sugar over Node.Send / Node.CloseSession by SessionID; Session
// itself carries no mutable state, so passing it by value avoids the
// heap escape that a *Session would force on every Connect call.
type Session struct {
	Peer       identity.Hash
	SessionID  SessionID
	PeerPublic identity.PublicIdentity

	node *Node
}

// Send delegates to Node.Send for this session's (peer, sid).
func (s Session) Send(ctx context.Context, payload []byte) error {
	return s.node.Send(ctx, s.Peer, s.SessionID, payload)
}

// Close delegates to Node.CloseSession for this session's (peer, sid).
func (s Session) Close() error {
	return s.node.CloseSession(s.Peer, s.SessionID)
}
