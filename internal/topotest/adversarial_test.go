package topotest_test

import (
	"context"
	crand "crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/topotest"
	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
)

// §6 — adversarial tests. Each test isolates a specific attack vector
// and asserts the system's defence holds: subnet cap (S/Kademlia
// §4.2), unverified-contact rule, lookup robustness against garbage
// FIND_NODE responses.

// TestAdversarial_SubnetCap_PreventsSybilCluster verifies the
// per-/24 cap on routing-table buckets: even if N attacker contacts
// land in the same bucket from the same /24, only
// MaxContactsPerSubnet survive. Without this cap, a single
// attacker-controlled host could claim arbitrary NodeIDs and
// monopolise a bucket.
//
// We test directly via RoutingTable.Add — bypassing transport — to
// pin the invariant. A network-level Sybil (many transports forging
// FIND_NODE) is harder to script in-process and is covered by
// integration tests in pkg/dht/bucket_subnet_test.go.
func TestAdversarial_SubnetCap_PreventsSybilCluster(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{})
	c.Spawn(t, 1)
	victim := c.Peer(0).Node()

	// Craft 10 attacker contacts: distinct NodeIDs all close to one
	// another (so they fall into the same bucket), all from the same
	// /24. Pre-fill the IDs so they share a high prefix matching the
	// victim's ID — that forces them into one bucket.
	self := victim.ID()
	var base dht.NodeID
	copy(base[:], self[:])
	base[0] ^= 0x01 // flip 1 bit so they're not == self

	for i := range 10 {
		var fake dht.NodeID
		copy(fake[:], base[:])
		fake[15] = byte(i) // last byte varies — same bucket
		victim.Table().Add(dht.Contact{
			ID: fake,
			Addr: &net.UDPAddr{
				IP:   net.IPv4(203, 0, 113, byte(100+i)),
				Port: 9000,
			},
		})
	}

	// Count how many contacts from 203.0.113.0/24 actually made it
	// in. The cap is per-bucket, not global, but in this construction
	// they're all in the same bucket → no more than
	// MaxContactsPerSubnet.
	var fromSubnet int
	for _, ct := range victim.Table().All() {
		if udp, ok := ct.Addr.(*net.UDPAddr); ok {
			v4 := udp.IP.To4()
			if v4 != nil && v4[0] == 203 && v4[1] == 0 && v4[2] == 113 {
				fromSubnet++
			}
		}
	}

	if fromSubnet > dht.MaxContactsPerSubnet {
		t.Fatalf("subnet cap breached: %d contacts from 203.0.113.0/24 in routing table (max %d)",
			fromSubnet, dht.MaxContactsPerSubnet)
	}
}

// TestAdversarial_GarbageContactsInResponse: an attacker-controlled
// peer answers every FIND_NODE with a NodesMsg containing contacts
// whose addresses point nowhere (mem:does-not-exist-N). The honest
// requester's iterative lookup attempts those addresses, Send fails
// because the hub does not know them, and lookup completes within
// budget instead of hanging.
//
// We make the malicious peer the seed for a fresh requester so its
// initial shortlist is dominated by garbage. The lookup must not
// crash, and must return cleanly.
func TestAdversarial_GarbageContactsInResponse(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		RequestTimeout: 200 * time.Millisecond,
		LookupTimeout:  4 * time.Second,
	})
	c.Spawn(t, 1) // peer 0 = honest victim/requester

	// Build a malicious raw transport on the same hub.
	mal := c.Hub().NewMemoryTransport()
	t.Cleanup(func() { _ = mal.Close() })

	malID, err := identity.Generate(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	malNodeID := malID.Public().DestinationHash()

	// Goroutine: decode incoming FIND_NODE / PING, reply with junk.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go runMaliciousResponder(ctx, mal, malNodeID)

	// Honest peer pings malicious peer first, learns of it. Use a
	// short context: the malicious peer DOES reply to PING, so this
	// succeeds.
	pctx, pcancel := context.WithTimeout(t.Context(), 1*time.Second)
	if err := c.Peer(0).Node().Ping(pctx, mal.LocalAddr()); err != nil {
		pcancel()
		t.Fatalf("ping malicious peer: %v", err)
	}
	pcancel()

	// Lookup something. The shortlist will be dominated by the
	// malicious peer; its NodesMsg responses contain fake addresses
	// that fail Send. Lookup must terminate within LookupTimeout.
	target := dht.NodeID{}
	for i := range target {
		target[i] = 0xAA
	}
	lctx, lcancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer lcancel()
	start := time.Now()
	_, err = c.Peer(0).Node().LookupNode(lctx, target)
	elapsed := time.Since(start)
	// Lookup must not hang. err is allowed (target doesn't exist),
	// the assertion is on time and absence of crash.
	if elapsed > 5*time.Second {
		t.Fatalf("lookup took %v, expected to terminate within budget", elapsed)
	}
}

// runMaliciousResponder is the malicious peer's behaviour: respond
// to PING with a real PONG (so victim adds us to its routing table)
// but answer every FIND_NODE with junk contacts that point to
// non-existent hub IDs.
func runMaliciousResponder(ctx context.Context, tr *transport.MemoryTransport, malID dht.NodeID) {
	for {
		select {
		case <-ctx.Done():
			return
		case pkt, ok := <-tr.Inbox():
			if !ok {
				return
			}
			payload := append([]byte(nil), pkt.Payload...)
			pkt.Release()
			msg, err := dht.DecodeMsg(payload)
			if err != nil {
				continue
			}
			switch m := msg.(type) {
			case *dht.PingMsg:
				resp := &dht.PongMsg{
					Header: dht.Header{
						TxID:    m.Header.TxID,
						SrcID:   malID,
						SrcAddr: tr.LocalAddr().String(),
					},
				}
				blob, err := dht.EncodeMsg(resp)
				if err == nil {
					_ = tr.Send(ctx, pkt.From, blob)
				}
			case *dht.FindNodeMsg:
				// Junk: 8 contacts whose Addr points to non-existent
				// hub ids.
				junk := make([]dht.EncodedContact, 8)
				for i := range junk {
					var id dht.NodeID
					id[0] = 0xDE
					id[1] = 0xAD
					id[2] = byte(i)
					junk[i] = dht.EncodedContact{
						ID:   id,
						Addr: "mem:does-not-exist-" + string(rune('A'+i)),
					}
				}
				resp := &dht.NodesMsg{
					Header: dht.Header{
						TxID:    m.Header.TxID,
						SrcID:   malID,
						SrcAddr: tr.LocalAddr().String(),
					},
					Contacts: junk,
				}
				blob, err := dht.EncodeMsg(resp)
				if err == nil {
					_ = tr.Send(ctx, pkt.From, blob)
				}
			}
		}
	}
}

// TestAdversarial_HonestPathSurvives: the honest peer sees both an
// honest seed AND a malicious one. The malicious peer hands out junk;
// the honest seed knows the real target. With S/Kademlia disjoint
// paths the honest path should still find the target within budget.
func TestAdversarial_HonestPathSurvives(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		RequestTimeout: 200 * time.Millisecond,
		LookupTimeout:  6 * time.Second,
	})
	// Peer 0 = requester, peer 1 = honest seed, peer 2 = real target.
	c.Spawn(t, 3)

	// Honest topology: seed knows target, requester knows seed.
	c.Connect(t, 1, 2)
	topotest.WaitFor(t, 1*time.Second, func() bool {
		return c.KnowsAbout(2, 1)
	})
	c.Connect(t, 0, 1)
	topotest.WaitFor(t, 1*time.Second, func() bool {
		return c.KnowsAbout(1, 0)
	})

	// Malicious peer joins as known to the requester.
	mal := c.Hub().NewMemoryTransport()
	t.Cleanup(func() { _ = mal.Close() })
	malID, _ := identity.Generate(crand.Reader)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go runMaliciousResponder(ctx, mal, malID.Public().DestinationHash())

	pctx, pcancel := context.WithTimeout(t.Context(), 1*time.Second)
	if err := c.Peer(0).Node().Ping(pctx, mal.LocalAddr()); err != nil {
		pcancel()
		t.Fatalf("ping malicious peer: %v", err)
	}
	pcancel()

	// Now lookup peer 2's ID. Despite the malicious noise, the
	// honest path through peer 1 must succeed.
	lctx, lcancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer lcancel()
	closest, err := c.Peer(0).Node().LookupNode(lctx, c.Peer(2).ID())
	if err != nil {
		t.Fatalf("lookup with malicious peer in shortlist: %v", err)
	}
	target := c.Peer(2).ID()
	found := false
	for _, ct := range closest {
		if ct.ID == target {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("honest path failed to find target despite disjoint paths (got %d closest)", len(closest))
	}
}
