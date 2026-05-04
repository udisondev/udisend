package network

import (
	"context"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
)

// PeerInfo is a snapshot of a peer's presence record. Returned by value
// — small (~120 bytes), read-only, no mutex — so a *PeerInfo would only
// force the value onto the heap with no upside.
type PeerInfo struct {
	Hash        identity.Hash
	Public      identity.PublicIdentity
	Address     string
	IssuedAt    time.Time
}

// Lookup resolves a peer's presence record via the DHT.
func (n *Node) Lookup(ctx context.Context, peer identity.Hash) (PeerInfo, error) {
	rec, err := n.resolver.Lookup(ctx, peer)
	if err != nil {
		return PeerInfo{}, err
	}

	return PeerInfo{
		Hash:     peer,
		Public:   rec.Public,
		Address:  rec.Address,
		IssuedAt: rec.IssuedAt,
	}, nil
}
