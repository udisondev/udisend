package network

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/udisondev/udisend/pkg/presence"
)

// DefaultSTUNPort is the well-known STUN listen port (RFC 5389).
// Records advertise their DHT/signaling socket; the STUN responder runs
// on this fixed port at the same host.
const DefaultSTUNPort = "3478"

// ICECandidate is a domain-neutral STUN/TURN volunteer descriptor.
// Higher layers map it to the browser-friendly RTCIceServer JSON.
type ICECandidate struct {
	Host string
	Port string
	Kind string // "stun" | "turn"
}

// ICEServers discovers STUN/TURN volunteers in the routing table by
// resolving each known contact's presence record and filtering by
// Capability bits. Best-effort: peers that fail to resolve within
// the per-peer lookup TTL are skipped silently.
//
// design.md §6: client picks 3-5 servers and passes them as ICEServers
// to the browser's RTCPeerConnection.
func (n *Node) ICEServers(ctx context.Context, max int) []ICECandidate {
	if max <= 0 {
		return nil
	}

	const lookupTTL = 1500 * time.Millisecond

	contacts := n.dht.Table().All()
	if len(contacts) == 0 {
		return nil
	}

	// Cancellable child ctx so once we have enough servers, in-flight
	// goroutines waiting on resolver.Lookup return immediately rather
	// than burn the full lookupTTL.
	gather, cancel := context.WithCancel(ctx)
	defer cancel()

	type result struct {
		cand ICECandidate
	}
	results := make(chan result, len(contacts))

	var wg sync.WaitGroup
	for _, c := range contacts {
		wg.Go(func() {
			rctx, rcancel := context.WithTimeout(gather, lookupTTL)
			defer rcancel()

			rec, err := n.resolver.Lookup(rctx, c.ID)
			if err != nil {
				// Per-peer lookup misses are expected (cold cache,
				// peer offline). We log at Debug so a flood of
				// unreachable contacts is observable but not noisy.
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, presence.ErrNotFound) {
					n.cfg.Logger.Debug("network: ICE candidate lookup failed", "peer", c.ID, "err", err)
				}

				return
			}
			if rec == nil {
				return
			}
			if !rec.Capabilities.Has(presence.CapCanSTUN) {
				return
			}

			host, _, splitErr := net.SplitHostPort(rec.Address)
			if splitErr != nil {
				// Address was advertised without a port — fall back to
				// the bare host. We still log at Debug because the
				// presence record format is supposed to include a port.
				n.cfg.Logger.Debug("network: ICE candidate address parse", "peer", c.ID, "addr", rec.Address, "err", splitErr)
				host = rec.Address
			}

			results <- result{cand: ICECandidate{Host: host, Port: DefaultSTUNPort, Kind: "stun"}}
		})
	}
	go func() { wg.Wait(); close(results) }()

	out := make([]ICECandidate, 0, max)
	for r := range results {
		out = append(out, r.cand)
		if len(out) >= max {
			cancel()
			break
		}
	}

	return out
}
