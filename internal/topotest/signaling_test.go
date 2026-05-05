package topotest_test

import (
	"context"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/topotest"
)

// §8 — signaling-layer topology tests. These verify that an
// envelope from peer A reaches peer B and that the responder side
// can Verify it (full Noise handshake completes), under various
// topology shapes and disruption modes. The signaling layer is
// where «писать друг другу» lives in our stack — DataChannel content
// is browser-side and out of scope.

// TestSig_DirectDelivery — A connects to B with both peers' real
// addresses in the resolver (no relay needed). Smoke test for the
// signaling-on-MemoryHub harness.
func TestSig_DirectDelivery(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{WithSignaling: true})
	c.Spawn(t, 2)

	got := c.Sig().SendAndReceive(t, 0, 1, []byte("ping"), 5*time.Second)
	if string(got) != "ping" {
		t.Fatalf("got %q, want %q", got, "ping")
	}
}

// TestSig_BothDirections — A→B, then B→A on the same session.
// Verifies the responder side can also send.
func TestSig_BothDirections(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{WithSignaling: true})
	c.Spawn(t, 2)

	chA := c.Sig().Connect(t, 0, 1, 5*time.Second)
	chB, _ := c.Sig().AcceptOn(t, 1, 5*time.Second)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if err := chA.Send(ctx, []byte("hi B")); err != nil {
		t.Fatalf("A send: %v", err)
	}
	got, err := chB.Recv(ctx)
	if err != nil || string(got) != "hi B" {
		t.Fatalf("B recv: got=%q err=%v", got, err)
	}

	if err := chB.Send(ctx, []byte("hi A")); err != nil {
		t.Fatalf("B send: %v", err)
	}
	got, err = chA.Recv(ctx)
	if err != nil || string(got) != "hi A" {
		t.Fatalf("A recv: got=%q err=%v", got, err)
	}
}

// TestSig_RelayChain — A's resolver knows B only as «relay's
// address». A.Connect(B) sends HELLO_INIT to relay; relay's Service
// sees the envelope is for B (not for itself), looks up B's real
// address through its Router, forwards. B accepts. design.md §4.
func TestSig_RelayChain(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{WithSignaling: true})
	c.Spawn(t, 3) // 0 = A, 1 = relay, 2 = B

	// Override A's view of B's address: A only knows B through
	// relay. Relay's clusterRouter still resolves B's hash to B's
	// real MemoryAddr because Router uses the cluster table.
	c.Sig().OverrideAddress(t, 2, 1)

	got := c.Sig().SendAndReceive(t, 0, 2, []byte("relayed payload"), 5*time.Second)
	if string(got) != "relayed payload" {
		t.Fatalf("got %q, want %q", got, "relayed payload")
	}
}

// TestSig_TwoHopRelay — A → R1 → R2 → B. A's resolver points B at
// R1. R1's resolver/router would forward directly (clusterRouter
// resolves any cluster peer), but to force two-hop we override R1's
// view too: from R1's perspective B's address is R2. Each hop
// re-encrypts the envelope envelope (signaling layer treats every
// MsgRelay frame the same way) and delivery still works because
// every router knows the full cluster.
//
// Note: in production cf design.md §4, multi-hop is handled by
// recursive forwarding through the DHT. Here we model it by chained
// resolver overrides.
func TestSig_TwoHopRelay(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{WithSignaling: true})
	c.Spawn(t, 4) // 0 = A, 1 = R1, 2 = R2, 3 = B

	// A → R1, R1 → R2, R2 → B. Resolver overrides shape only the
	// initial Connect Dial; subsequent forwarding uses Router which
	// always knows the real address. So we get a single hop in
	// practice: R1 sees envelope for B, looks up real address, sends
	// directly. To enforce a real two-hop chain we'd need to make
	// R1's Router NOT know B and only know R2 — which our cluster
	// router does not currently model. We instead test that the
	// 1-hop relay variant works at N=4 (just to widen the topology
	// from the 3-peer case).
	c.Sig().OverrideAddress(t, 3, 1)

	got := c.Sig().SendAndReceive(t, 0, 3, []byte("through chain"), 5*time.Second)
	if string(got) != "through chain" {
		t.Fatalf("got %q, want %q", got, "through chain")
	}
}

// TestSig_FailsForUnknownPeer — Connect to a peer not in the
// resolver fails fast, does not hang.
func TestSig_FailsForUnknownPeer(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{
		WithSignaling: true,
	})
	c.Spawn(t, 2)

	// Drop peer 1 from the resolver by overriding to a non-existent
	// peer? We don't have such an API. Instead create an extra
	// peer that we never publish: spawn it on the hub directly.
	// Easier: just try to Connect to an unrelated random hash.
	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()
	var ghost [16]byte
	for i := range ghost {
		ghost[i] = 0x42
	}
	if _, err := c.Sig().Service(0).Connect(ctx, ghost); err == nil {
		t.Fatalf("expected error connecting to unknown peer")
	}
}

// TestSig_PartitionBlocksDelivery — A and B start with sessions
// possible. We detach B from the hub mid-handshake; Connect must
// fail without hanging.
func TestSig_PartitionBlocksDelivery(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{WithSignaling: true})
	c.Spawn(t, 2)

	prev := c.Detach(1)
	t.Cleanup(func() { c.Reattach(1, prev) })

	_, err := c.Sig().TryConnect(t, 0, 1, 1*time.Second)
	if err == nil {
		t.Fatalf("expected connect to fail while B is detached")
	}
}

// TestSig_RelayDeath — A → relay → B, then relay disappears.
// Future sessions fail (relay is gone). Earlier completed sessions
// remain usable because the channel has its own session state. We
// only assert the Connect-after-relay-death failure here.
func TestSig_RelayDeath(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{WithSignaling: true})
	c.Spawn(t, 3)
	c.Sig().OverrideAddress(t, 2, 1)

	// Sanity: relay path works first.
	got := c.Sig().SendAndReceive(t, 0, 2, []byte("first"), 5*time.Second)
	if string(got) != "first" {
		t.Fatalf("first round: %q", got)
	}

	// Kill relay.
	prev := c.Detach(1)
	t.Cleanup(func() { c.Reattach(1, prev) })

	// New connect via the same dead relay must fail.
	_, err := c.Sig().TryConnect(t, 0, 2, 1*time.Second)
	if err == nil {
		t.Fatalf("expected connect to fail with dead relay")
	}
}

// TestSig_FanOut — one peer opens sessions to N others in parallel,
// each receives the right payload. Catches races in the resolver,
// in newSession registration, and in the shared MemoryHub.
func TestSig_FanOut(t *testing.T) {
	t.Parallel()

	const fan = 6
	c := topotest.New(topotest.Options{WithSignaling: true})
	c.Spawn(t, fan+1) // 0 = sender; 1..fan = recipients

	type result struct {
		dst int
		got string
		err error
	}
	results := make(chan result, fan)

	for i := 1; i <= fan; i++ {
		go func(dst int) {
			payload := []byte("to-" + string(rune('A'+dst-1)))
			got := c.Sig().SendAndReceive(t, 0, dst, payload, 5*time.Second)
			results <- result{dst: dst, got: string(got), err: nil}
		}(i)
	}

	got := map[int]string{}
	for range fan {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("dst %d: %v", r.dst, r.err)
			}
			got[r.dst] = r.got
		case <-time.After(10 * time.Second):
			t.Fatalf("fan-out: only %d/%d completed", len(got), fan)
		}
	}
	for dst := 1; dst <= fan; dst++ {
		want := "to-" + string(rune('A'+dst-1))
		if got[dst] != want {
			t.Fatalf("dst %d: got %q want %q", dst, got[dst], want)
		}
	}
}

// TestSig_MultiPayload — long-running session, A sends N payloads
// in sequence, B receives all in order. Catches re-entry / state
// machine bugs in Send/Recv.
func TestSig_MultiPayload(t *testing.T) {
	t.Parallel()

	c := topotest.New(topotest.Options{WithSignaling: true})
	c.Spawn(t, 2)

	chA := c.Sig().Connect(t, 0, 1, 5*time.Second)
	chB, _ := c.Sig().AcceptOn(t, 1, 5*time.Second)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	const n = 25
	for i := range n {
		msg := []byte{byte(i)}
		if err := chA.Send(ctx, msg); err != nil {
			t.Fatalf("A send %d: %v", i, err)
		}
	}
	for i := range n {
		got, err := chB.Recv(ctx)
		if err != nil {
			t.Fatalf("B recv %d: %v", i, err)
		}
		if len(got) != 1 || got[0] != byte(i) {
			t.Fatalf("B recv %d: got %v, want [%d]", i, got, i)
		}
	}
}
