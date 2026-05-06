package dht_test

import (
	"context"
	"crypto/rand"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
)

type peer struct {
	node *dht.Node
	t    transport.Transport
	id   *identity.Identity
	stop context.CancelFunc
}

type harness struct {
	hub   *transport.MemoryHub
	peers []*peer
	wg    sync.WaitGroup
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{hub: transport.NewMemoryHub()}
}

func (h *harness) spawn(t *testing.T, seed io.Reader) *peer {
	t.Helper()
	id, err := identity.Generate(seed)
	if err != nil {
		t.Fatal(err)
	}
	mt := h.hub.NewMemoryTransport()
	node := dht.NewNode(id, mt, nil, dht.Config{
		RequestTimeout: 500 * time.Millisecond,
		LookupTimeout:  3 * time.Second,
	})
	ctx, cancel := context.WithCancel(t.Context())
	p := &peer{node: node, t: mt, id: id, stop: cancel}
	h.peers = append(h.peers, p)
	h.wg.Go(func() {
		node.Run(ctx)
	})
	t.Cleanup(func() {
		cancel()
		_ = mt.Close()
	})
	return p
}

func (h *harness) wait() {
	for _, p := range h.peers {
		p.stop()
	}
	for _, p := range h.peers {
		_ = p.t.Close()
	}
	h.wg.Wait()
}

func TestPing_RoundTrip(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		a := h.spawn(t, rand.Reader)
		b := h.spawn(t, rand.Reader)
		defer h.wait()

		if err := a.node.Ping(t.Context(), b.t.LocalAddr()); err != nil {
			t.Fatalf("ping: %v", err)
		}
		// `a` learns about `b` synchronously through the PongMsg it just
		// received.
		if a.node.Table().Size() == 0 {
			t.Errorf("a.table empty")
		}
		// `b` learns about `a` ASYNCHRONOUSLY: receiving a PingMsg schedules
		// a verification probe (`maybeProbe` → outbound Ping) and `b` adds
		// `a` only after that probe's PONG comes back. Inside the synctest
		// bubble, synctest.Wait blocks until every goroutine is either
		// waiting on virtual time or on a blocking I/O syscall — the probe
		// completes deterministically.
		synctest.Wait()
		if b.node.Table().Size() == 0 {
			t.Errorf("b.table empty after maybeProbe window")
		}
	})
}

func TestBootstrap_ConvergesSmallNetwork(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const N = 6
	peers := make([]*peer, N)
	for i := range N {
		peers[i] = h.spawn(t, rand.Reader)
	}
	defer h.wait()

	// Connect everyone to peer 0.
	root := peers[0]
	for i := 1; i < N; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		if err := peers[i].node.Bootstrap(ctx, root.t.LocalAddr()); err != nil {
			cancel()
			t.Fatalf("bootstrap %d: %v", i, err)
		}
		cancel()
	}

	// Now any peer should be able to look up any other peer's contact via
	// iterative FIND_NODE.
	for i, p := range peers {
		for j, q := range peers {
			if i == j {
				continue
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			closest, err := p.node.LookupNode(ctx, q.node.ID())
			cancel()
			if err != nil {
				t.Fatalf("lookup %d→%d: %v", i, j, err)
			}
			found := false
			for _, c := range closest {
				if c.ID == q.node.ID() {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("peer %d failed to find peer %d (got %d closest)", i, j, len(closest))
			}
		}
	}
}

// TestRouting_DoesNotAdmitSilentRequester covers the canonical
// Kademlia rule made explicit by the 2026-05-05 audit: a peer that
// only sends us request-path messages but never responds to our
// verification probe MUST NOT appear in our routing table. The fix
// asks "do you actually live at this address?" via maybeProbe; a
// silent address never gets added.
//
// We craft the request using a fake transport that drops everything
// inbound to itself, so the legitimate node's PING-back goes
// unanswered. Without the fix, the routing table would have a fake
// entry for `attacker.ID` chosen close to the victim's hash.
func TestRouting_DoesNotAdmitSilentRequester(t *testing.T) {
	t.Parallel()
	// synctest virtualises time inside the bubble: the 900 ms wait for
	// the victim's probe to time out (RequestTimeout=500 ms) compresses
	// to ~0 wall-clock once every goroutine in the bubble is durably
	// blocked. All node goroutines spawned via the harness inside this
	// closure are part of the bubble.
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		victim := h.spawn(t, rand.Reader)
		defer h.wait()

		// Attacker-controlled transport — listens but drops every packet.
		att := h.hub.NewMemoryTransport()
		t.Cleanup(func() { _ = att.Close() })
		go func() {
			for range att.Inbox() {
				// Drop every packet — silently absorb the victim's PING.
			}
		}()

		// Forge a FindNodeMsg with a chosen SrcID. Use the same wire
		// helpers the real attacker would.
		tx, err := dht.NewTxID()
		if err != nil {
			t.Fatal(err)
		}
		var spoofedID dht.NodeID
		spoofedID[0] = 0xAA
		msg := &dht.FindNodeMsg{
			Header: dht.Header{
				TxID:    tx,
				SrcID:   spoofedID,
				SrcAddr: att.LocalAddr().String(),
			},
			Target: victim.node.ID(),
		}
		blob, err := dht.EncodeMsg(msg)
		if err != nil {
			t.Fatal(err)
		}

		if err := att.Send(t.Context(), victim.t.LocalAddr(), blob); err != nil {
			t.Fatal(err)
		}

		// Wait for the victim's probe to time out (RequestTimeout=500 ms in
		// the test harness). Anything well past that is enough.
		time.Sleep(900 * time.Millisecond)

		for _, c := range victim.node.Table().All() {
			if c.ID == spoofedID {
				t.Fatalf("victim admitted unverified SrcID %x — request-path bypass", spoofedID)
			}
		}
	})
}

func TestPutValue_AndLookup(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const N = 5
	peers := make([]*peer, N)
	for i := range N {
		peers[i] = h.spawn(t, rand.Reader)
	}
	defer h.wait()

	for i := 1; i < N; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		if err := peers[i].node.Bootstrap(ctx, peers[0].t.LocalAddr()); err != nil {
			cancel()
			t.Fatalf("bootstrap %d: %v", i, err)
		}
		cancel()
	}

	key := identity.Hash{}
	copy(key[:], []byte("hello-key-12345!"))
	value := []byte("the value")

	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	if err := peers[2].node.PutValue(ctx, key, value); err != nil {
		cancel()
		t.Fatalf("PutValue: %v", err)
	}
	cancel()

	// Look up from a peer that didn't initiate the put.
	ctx, cancel = context.WithTimeout(t.Context(), 4*time.Second)
	got, _, err := peers[4].node.LookupValue(ctx, key)
	cancel()
	if err != nil {
		t.Fatalf("LookupValue: %v", err)
	}
	if string(got) != string(value) {
		t.Fatalf("got %q, want %q", got, value)
	}
}
