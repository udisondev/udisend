package network

import (
	"context"
	"net"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
)

// routingBackend is the subset of dht.Node behaviour dhtRouter needs.
// Interface form lets unit tests substitute a stub.
type routingBackend interface {
	Closest(target identity.Hash, n int) []dht.Contact
	LookupNode(ctx context.Context, target identity.Hash) ([]dht.Contact, error)
}

const defaultPathLookupTimeout = 1500 * time.Millisecond

// dhtRouter satisfies signaling.Router via routingBackend, with a bounded
// iterative-lookup fallback (design.md §4 path requests).
type dhtRouter struct {
	backend routingBackend
	timeout time.Duration
}

func newDHTRouter(node *dht.Node) dhtRouter {
	return dhtRouter{backend: nodeRouting{node}, timeout: defaultPathLookupTimeout}
}

// NextHop resolves a recipient destination hash to the address of the
// next routing hop. Fast path: routing table has `target` directly.
// Slow path: bounded iterative LookupNode tries to discover a route
// through the DHT. If lookup returns nothing, fall back to the closest
// known contact (best-effort hop). Returns false only when both the
// local table is empty and lookup yields nothing.
func (r dhtRouter) NextHop(ctx context.Context, target identity.Hash) (net.Addr, bool) {
	closest := r.backend.Closest(target, 1)
	if len(closest) > 0 && closest[0].ID == target {
		return closest[0].Addr, true
	}

	lctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// LookupNode failure (timeout, transport error, ctx cancel) is
	// indistinguishable from "no result" for the caller: we either
	// learned a route or we did not. The bool return surfaces that
	// outcome — the underlying error has no actionable diagnostic
	// value here because we still fall back to the closest known
	// contact below.
	found, err := r.backend.LookupNode(lctx, target)
	if err == nil {
		for _, c := range found {
			if c.ID == target {
				return c.Addr, true
			}
		}
		if len(found) > 0 {
			return found[0].Addr, true
		}
	}
	if len(closest) > 0 {
		return closest[0].Addr, true
	}

	return nil, false
}

// LocalNextHop is the cache-only path used for forwarding envelopes
// originated by remote peers (relay path). A miss must drop rather
// than trigger iterative DHT search — the iterative path can spawn
// up to alpha × disjoint outbound FIND_NODEs per request, turning
// any stranger into a bandwidth-amplification source.
func (r dhtRouter) LocalNextHop(target identity.Hash) (net.Addr, bool) {
	closest := r.backend.Closest(target, 1)
	if len(closest) > 0 && closest[0].ID == target {
		return closest[0].Addr, true
	}

	return nil, false
}

// nodeRouting wraps *dht.Node into routingBackend.
type nodeRouting struct{ n *dht.Node }

func (b nodeRouting) Closest(target identity.Hash, n int) []dht.Contact {
	return b.n.Table().Closest(target, n)
}

func (b nodeRouting) LookupNode(ctx context.Context, target identity.Hash) ([]dht.Contact, error) {
	return b.n.LookupNode(ctx, target)
}

// nodeAdapter exposes the subset of dht.Node required by presence.DHT.
type nodeAdapter struct{ n *dht.Node }

func (a nodeAdapter) PutValue(ctx context.Context, key dht.NodeID, value []byte) error {
	return a.n.PutValue(ctx, key, value)
}

func (a nodeAdapter) LookupValue(ctx context.Context, key dht.NodeID) ([]byte, []dht.Contact, error) {
	return a.n.LookupValue(ctx, key)
}
