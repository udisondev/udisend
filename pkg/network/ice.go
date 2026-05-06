package network

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	pionstun "github.com/pion/stun/v3"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
)

// DefaultSTUNPort is the well-known STUN listen port (RFC 5389).
// Records advertise their DHT/signaling socket; the STUN responder runs
// on this fixed port at the same host.
const DefaultSTUNPort = "3478"

// stunProbeTimeout caps the time spent verifying a single advertised
// STUN responder. Real STUN servers reply in tens of milliseconds; 500
// ms is generous against honest jitter and short against a bogus or
// dead advertiser. Probing happens in parallel for each candidate so
// the wall-clock cost of vetting N advertisers is bounded by this
// constant, not by N.
const stunProbeTimeout = 500 * time.Millisecond

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
// the per-peer lookup TTL are skipped silently. The client picks
// 3-5 servers and passes them as ICEServers to the browser's
// RTCPeerConnection.
func (n *Node) ICEServers(ctx context.Context, max int) []ICECandidate {
	if max <= 0 {
		return nil
	}

	contacts := n.dht.Table().All()
	if len(contacts) == 0 {
		return nil
	}

	// Cancellable child ctx so once we have enough servers, in-flight
	// goroutines waiting on resolver.Lookup return immediately rather
	// than burn the full lookupTTL.
	gather, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan ICECandidate, len(contacts))
	var wg sync.WaitGroup
	for _, c := range contacts {
		wg.Go(func() {
			cand, ok := n.probeICECandidate(gather, c.ID)
			if !ok {
				return
			}
			results <- cand
		})
	}
	go func() { wg.Wait(); close(results) }()

	out := make([]ICECandidate, 0, max)
	for cand := range results {
		out = append(out, cand)
		if len(out) >= max {
			cancel()
			break
		}
	}

	return out
}

// iceLookupTTL caps how long resolver.Lookup may run for a single peer
// during ICE discovery; cancelled early once `max` candidates land.
const iceLookupTTL = 1500 * time.Millisecond

// probeICECandidate resolves peer's presence record and verifies it
// runs a working STUN responder. Returns (zero, false) on any failure
// (lookup miss, missing capability, unreachable probe).
func (n *Node) probeICECandidate(ctx context.Context, peer identity.PeerID) (ICECandidate, bool) {
	rctx, cancel := context.WithTimeout(ctx, iceLookupTTL)
	defer cancel()

	rec, err := n.resolver.Lookup(rctx, peer)
	if err != nil {
		// Per-peer lookup misses are expected (cold cache, peer offline).
		// We log at Debug so a flood of unreachable contacts is observable
		// but not noisy.
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, presence.ErrNotFound) {
			n.cfg.Logger.Debug("network: ICE candidate lookup failed", "peer", peer, "err", err)
		}

		return ICECandidate{}, false
	}
	if rec == nil || !rec.Capabilities.Has(presence.CapCanSTUN) {
		return ICECandidate{}, false
	}

	host, _, splitErr := net.SplitHostPort(rec.Address)
	if splitErr != nil {
		// Address was advertised without a port — fall back to the bare
		// host. Logged at Debug because the presence record format is
		// supposed to include a port.
		n.cfg.Logger.Debug("network: ICE candidate address parse", "peer", peer, "addr", rec.Address, "err", splitErr)
		host = rec.Address
	}

	// Capability-spoofing defence: a peer behind NAT can publish a
	// presence record with CapCanSTUN and a victim's IP, directing
	// every client's RTCPeerConnection to probe the victim. The
	// signature only proves *who* set the bit, not that the address
	// is actually a working STUN responder. Probe before trusting.
	probeAddr := net.JoinHostPort(host, DefaultSTUNPort)
	pctx, pcancel := context.WithTimeout(rctx, stunProbeTimeout)
	err = probeSTUNReachable(pctx, probeAddr)
	pcancel()
	if err != nil {
		n.cfg.Logger.Debug("network: ICE STUN probe failed", "peer", peer, "addr", probeAddr, "err", err)

		return ICECandidate{}, false
	}

	return ICECandidate{Host: host, Port: DefaultSTUNPort, Kind: "stun"}, true
}

// probeSTUNReachable sends a STUN binding request to addr and returns
// nil iff a STUN binding-response with matching transaction ID arrives
// before ctx expires. The probe is intentionally lightweight: one
// 20-byte packet out, one short packet in, no retries. A non-responding
// advertiser is treated as bogus — the goal is to bind capability
// claims to actual reachability, not to perform an exhaustive STUN
// conformance check.
func probeSTUNReachable(ctx context.Context, addr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return err
		}
	}

	var txID [pionstun.TransactionIDSize]byte
	if _, err := io.ReadFull(rand.Reader, txID[:]); err != nil {
		return err
	}
	req, err := pionstun.Build(
		pionstun.NewTransactionIDSetter(txID),
		pionstun.BindingRequest,
		pionstun.Fingerprint,
	)
	if err != nil {
		return err
	}
	if _, err := conn.Write(req.Raw); err != nil {
		return err
	}

	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	if !pionstun.IsMessage(buf[:n]) {
		return errors.New("network: STUN probe got non-STUN response")
	}
	resp := &pionstun.Message{Raw: append([]byte{}, buf[:n]...)}
	if err := resp.Decode(); err != nil {
		return err
	}
	if resp.TransactionID != txID {
		return errors.New("network: STUN probe transaction-ID mismatch")
	}
	if resp.Type.Class != pionstun.ClassSuccessResponse || resp.Type.Method != pionstun.MethodBinding {
		return errors.New("network: STUN probe non-success response")
	}

	return nil
}
