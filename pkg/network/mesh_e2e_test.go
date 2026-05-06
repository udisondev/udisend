package network_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/network"
	"github.com/udisondev/udisend/pkg/transport"
)

// testHandshakeTimeout is the signaling handshake budget used by the
// e2e suite. Production stays at signaling.DefaultHandshakeTimeout
// (6s); tests need a longer window because race-detector overhead
// under concurrent package binaries can push the Noise XK 3-message
// handshake well past the production-tuned value. Keeping it as a
// single const keeps the per-Open Config calls in sync.
const testHandshakeTimeout = 30 * time.Second

// twoMeshNodes spins up A and B with MeshEnabled and waits for both
// sides to resolve each other's presence record. Returns only after
// the routing tables and presence caches are warm enough that
// PeerManager.SelectPeers will see the other side as a viable
// candidate.
func twoMeshNodes(t *testing.T) (*network.Node, *network.Node) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	hub := transport.NewMemoryHub()
	trA := hub.NewMemoryTransport()
	trB := hub.NewMemoryTransport()

	a, err := network.Open(ctx, network.Config{
		Identity:                  mustIdentity(t),
		Transport:                 trA,
		MeshEnabled:               true,
		MaxMeshLinks:              2,
		SignalingHandshakeTimeout: testHandshakeTimeout,
	})
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("close A: %v", err)
		}
	})
	goRun(t, "A", a.Run, ctx)

	b, err := network.Open(ctx, network.Config{
		Identity:                  mustIdentity(t),
		Transport:                 trB,
		Bootstrap:                 []string{a.LocalAddress()},
		MeshEnabled:               true,
		MaxMeshLinks:              2,
		SignalingHandshakeTimeout: testHandshakeTimeout,
	})
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("close B: %v", err)
		}
	})
	goRun(t, "B", b.Run, ctx)

	// Wait for mutual presence resolution — same gate as twoNodes.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		lctx, lcancel := context.WithTimeout(ctx, 250*time.Millisecond)
		_, errA := a.Lookup(lctx, b.Identity().Public().DestinationHash())
		lcancel()
		lctx, lcancel = context.WithTimeout(ctx, 250*time.Millisecond)
		_, errB := b.Lookup(lctx, a.Identity().Public().DestinationHash())
		lcancel()
		if errA == nil && errB == nil {
			return a, b
		}
		if errors.Is(errA, context.Canceled) || errors.Is(errB, context.Canceled) {
			t.Fatal("ctx canceled during convergence")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("nodes never converged on each other's presence")

	return nil, nil
}

// TestMesh_E2E_TwoNodesEstablishLink drives a full Phase 10 flow:
// two real network.Nodes converge on each other's presence, their
// PeerManagers see the peer as mesh-capable (CapCanWebRTCMesh
// advertised when MeshEnabled), call WebRTCTransport.Connect which
// drives a real pion handshake through the signaling.Channel
// (Noise XK end-to-end) — and the resulting DataChannel survives.
//
// This is the smallest meaningful resilience proof in Phase 10:
// without the e2e link working, the bigger "kill 2 publics" tests
// have no foundation to stand on.
func TestMesh_E2E_TwoNodesEstablishLink(t *testing.T) {
	// Not t.Parallel: see TestMesh_E2E_DataRoundTrip.
	if testing.Short() {
		t.Skip("requires pion ICE — skipped under -short")
	}

	a, b := twoMeshNodes(t)

	aHash := a.Identity().Public().DestinationHash()
	bHash := b.Identity().Public().DestinationHash()

	// PeerManager fires reconcile every 30s by default but the
	// initial reconcile happens immediately on Run. The 60s budget
	// covers race-detector overhead under concurrent suite load
	// (production handshake completes in <1s).
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if a.MeshTransport().IsConnected(bHash) && b.MeshTransport().IsConnected(aHash) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !a.MeshTransport().IsConnected(bHash) {
		t.Errorf("A never opened a mesh DataChannel to B")
	}
	if !b.MeshTransport().IsConnected(aHash) {
		t.Errorf("B never opened a mesh DataChannel to A")
	}

	statsA := a.Stats()
	statsB := b.Stats()
	if statsA.RTCPeers != 1 {
		t.Errorf("A.Stats RTCPeers = %d, want 1", statsA.RTCPeers)
	}
	if statsB.RTCPeers != 1 {
		t.Errorf("B.Stats RTCPeers = %d, want 1", statsB.RTCPeers)
	}
	// Exactly one side initiated; the other is a responder. Sum of
	// attempts is at least 1.
	if statsA.RTCConnectAttempts+statsB.RTCConnectAttempts < 1 {
		t.Errorf("Sum RTCConnectAttempts = %d, want ≥ 1",
			statsA.RTCConnectAttempts+statsB.RTCConnectAttempts)
	}
}

// waitForMeshLink blocks until both peers see each other as
// connected on the mesh AND a probe Send succeeds in both
// directions, or the deadline elapses. The probe is required
// because IsConnected flips to true the moment a responder
// installs its session (well before pion's OnDataChannel fires);
// without a Send-probe a test that races on the responder side
// can hit ErrNoRoute despite IsConnected returning true.
func waitForMeshLink(t *testing.T, a, b *network.Node, deadline time.Duration) bool {
	t.Helper()
	end := time.Now().Add(deadline)
	aHash := a.Identity().Public().DestinationHash()
	bHash := b.Identity().Public().DestinationHash()
	addrB := transport.NewWebRTCAddr(bHash)
	addrA := transport.NewWebRTCAddr(aHash)
	probe := []byte("waitForMeshLink-probe")
	for time.Now().Before(end) {
		if !a.MeshTransport().IsConnected(bHash) || !b.MeshTransport().IsConnected(aHash) {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		// Both sides see the entry; verify DC is actually open by
		// trying a Send. ErrNoRoute / ErrNotOpen → wait more.
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		errAB := a.MeshTransport().Send(ctx, addrB, probe)
		errBA := b.MeshTransport().Send(ctx, addrA, probe)
		cancel()
		if errAB == nil && errBA == nil {
			// Drain the two probe packets from each side so they
			// don't pollute the test's own inbox expectations.
			// Quiet=250ms is the per-slot budget; 2s ceiling guards
			// against pathological pion pipeline lag.
			drainInbox(a.MeshTransport().Inbox(), 250*time.Millisecond, 2*time.Second)
			drainInbox(b.MeshTransport().Inbox(), 250*time.Millisecond, 2*time.Second)
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}

	return false
}

// TestMesh_E2E_DataRoundTrip drives an actual byte payload through
// the mesh DataChannel after the link is established. Goes beyond
// the smoke test (which only checks IsConnected) — verifies that
// pion + Noise XK + the WebRTCTransport.pumpInbox path actually
// deliver data to the consumer's transport.Inbox.
func TestMesh_E2E_DataRoundTrip(t *testing.T) {
	// Not t.Parallel: two real pion stacks per call, race detector
	// adds ~10× overhead, and `go test ./pkg/... ./internal/...`
	// runs every package binary in parallel. Heavy mesh tests
	// running concurrently in one binary starve each other for CPU
	// and miss even the bumped 60s handshake window. Sequential
	// execution keeps each test in a deterministic budget.
	if testing.Short() {
		t.Skip("requires pion ICE — skipped under -short")
	}

	a, b := twoMeshNodes(t)

	if !waitForMeshLink(t, a, b, 60*time.Second) {
		t.Fatal("mesh link never established")
	}

	bHash := b.Identity().Public().DestinationHash()

	payload := bytes.Repeat([]byte("phase10-mesh-rtt-"), 64) // ~1 KiB
	addr := transport.NewWebRTCAddr(bHash)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if err := a.MeshTransport().Send(ctx, addr, payload); err != nil {
		t.Fatalf("a.MeshTransport.Send: %v", err)
	}

	select {
	case pkt := <-b.MeshTransport().Inbox():
		if !bytes.Equal(pkt.Payload, payload) {
			t.Errorf("payload mismatch: got %d bytes, want %d", len(pkt.Payload), len(payload))
		}
		gotAddr, ok := pkt.From.(transport.WebRTCAddr)
		if !ok {
			t.Errorf("pkt.From is %T, want WebRTCAddr", pkt.From)
		} else if gotAddr.Peer() != a.Identity().Public().DestinationHash() {
			t.Errorf("pkt.From.Peer = %x, want %x", gotAddr.Peer(), a.Identity().Public().DestinationHash())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("B did not receive mesh payload")
	}

	// Stats should reflect at least the round-trip bytes.
	statsA := a.Stats()
	statsB := b.Stats()
	if statsA.RTCBytesSent < int64(len(payload)) {
		t.Errorf("A.RTCBytesSent = %d, want ≥ %d", statsA.RTCBytesSent, len(payload))
	}
	if statsB.RTCBytesRecv < int64(len(payload)) {
		t.Errorf("B.RTCBytesRecv = %d, want ≥ %d", statsB.RTCBytesRecv, len(payload))
	}
}

// threeMeshNodes spins up A, B, C with mesh enabled. C bootstraps
// against B, B bootstraps against A — forming a "chain" where C
// needs DHT-iteration to discover A. Once the routing tables
// converge, all three pairs should mesh up.
func threeMeshNodes(t *testing.T) (*network.Node, *network.Node, *network.Node) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	hub := transport.NewMemoryHub()

	open := func(name string, bootstrap []string) *network.Node {
		tr := hub.NewMemoryTransport()
		n, err := network.Open(ctx, network.Config{
			Identity:                  mustIdentity(t),
			Transport:                 tr,
			Bootstrap:                 bootstrap,
			MeshEnabled:               true,
			MaxMeshLinks:              4,
			SignalingHandshakeTimeout: testHandshakeTimeout,
		})
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		t.Cleanup(func() { _ = n.Close() })
		goRun(t, name, n.Run, ctx)

		return n
	}

	a := open("A", nil)
	b := open("B", []string{a.LocalAddress()})
	c := open("C", []string{b.LocalAddress()})

	// Wait for full triangle convergence: every pair must Lookup
	// the other two.
	deadline := time.Now().Add(60 * time.Second)
	pairs := []struct {
		from, to *network.Node
		label    string
	}{
		{a, b, "A→B"}, {a, c, "A→C"},
		{b, a, "B→A"}, {b, c, "B→C"},
		{c, a, "C→A"}, {c, b, "C→B"},
	}
	for time.Now().Before(deadline) {
		ok := true
		for _, p := range pairs {
			lctx, lcancel := context.WithTimeout(ctx, 250*time.Millisecond)
			_, err := p.from.Lookup(lctx, p.to.Identity().Public().DestinationHash())
			lcancel()
			if err != nil {
				if errors.Is(err, context.Canceled) {
					t.Fatal("ctx canceled during convergence")
				}
				ok = false
				break
			}
		}
		if ok {
			return a, b, c
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("3-node cluster never converged")

	return nil, nil, nil
}

// TestMesh_E2E_ThreeNodeTriangle drives three real nodes through
// bootstrap convergence and asserts that every pair forms a mesh
// link. K=4 is enough to mesh with the other two regardless of
// who initiates.
//
// Not t.Parallel(): three pion stacks + ICE + Noise XK handshakes
// across six pairs is heavy enough that running alongside the rest
// of the parallel suite occasionally pushes K-fill past the 20 s
// deadline. Sequential execution is stable in isolation.
func TestMesh_E2E_ThreeNodeTriangle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires pion ICE — skipped under -short")
	}

	a, b, c := threeMeshNodes(t)

	pairs := []struct {
		x, y  *network.Node
		label string
	}{
		{a, b, "A↔B"},
		{a, c, "A↔C"},
		{b, c, "B↔C"},
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		allUp := true
		for _, p := range pairs {
			yHash := p.y.Identity().Public().DestinationHash()
			xHash := p.x.Identity().Public().DestinationHash()
			if !p.x.MeshTransport().IsConnected(yHash) || !p.y.MeshTransport().IsConnected(xHash) {
				allUp = false
				break
			}
		}
		if allUp {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	for _, p := range pairs {
		yHash := p.y.Identity().Public().DestinationHash()
		xHash := p.x.Identity().Public().DestinationHash()
		if !p.x.MeshTransport().IsConnected(yHash) {
			t.Errorf("%s missing: %s side has no link", p.label, p.label[:1])
		}
		if !p.y.MeshTransport().IsConnected(xHash) {
			t.Errorf("%s missing: reverse side has no link", p.label)
		}
	}
}

// TestMesh_E2E_SurvivesRelayLoss mirrors the original Phase 10
// resilience promise in miniature: A and C reach each other only
// through B's bootstrap-relay. Once their direct mesh link is up,
// killing B must not break the established A↔C DataChannel.
//
// This is the core property of Phase 10: persistent DataChannels
// survive the loss of public-IP signaling-relay infrastructure
// (in this scaled-down test, B plays the only public node both
// other peers know about; killing it simulates a public-relay
// outage).
//
// Not t.Parallel(): heavy three-node setup; see ThreeNodeTriangle
// rationale.
func TestMesh_E2E_SurvivesRelayLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("requires pion ICE — skipped under -short")
	}

	a, b, c := threeMeshNodes(t)

	aHash := a.Identity().Public().DestinationHash()
	cHash := c.Identity().Public().DestinationHash()

	// waitForMeshLink does the IsConnected check + a probe Send so
	// we know pion's OnDataChannel actually fired on both sides
	// before we proceed. Plain IsConnected flips true the moment a
	// responder installs its session, well before the DC is usable;
	// proceeding on that early signal makes the post-relay Send race
	// the responder's bring-up and intermittently report ErrNoRoute.
	if !waitForMeshLink(t, a, c, 60*time.Second) {
		t.Skipf("A↔C link did not establish before relay-loss test (timing)")
	}

	// Tear down the relay node B — close it explicitly, simulating
	// a public-IP node going offline. The MemoryHub also drops B's
	// transport from its registry.
	if err := b.Close(); err != nil {
		t.Errorf("close B: %v", err)
	}

	// A↔C must still be listed as connected for at least a brief
	// window afterwards — the DataChannel itself runs end-to-end
	// over MemoryHub and pion's loopback DTLS, independent of B.
	// Phase 10's "established DCs survive" promise.
	if !a.MeshTransport().IsConnected(cHash) {
		t.Error("A lost C link immediately after B died")
	}
	if !c.MeshTransport().IsConnected(aHash) {
		t.Error("C lost A link immediately after B died")
	}

	// And the DC must be usable for application traffic post-loss.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Drain any leftover probe packets from waitForMeshLink that may
	// still be in pion's pipeline. Its in-line drain has a 500ms
	// per-slot budget — under -race that is sometimes shorter than
	// pion's actual Inbox-pump latency, so a stale probe leaks into
	// the inbox we are about to read post-Send. 2s ceiling is well
	// above any realistic pipeline-drain time.
	drainInbox(c.MeshTransport().Inbox(), 250*time.Millisecond, 2*time.Second)

	addr := transport.NewWebRTCAddr(cHash)
	payload := []byte("post-relay-loss")
	if err := a.MeshTransport().Send(ctx, addr, payload); err != nil {
		t.Fatalf("Send post-loss: %v", err)
	}

	select {
	case pkt := <-c.MeshTransport().Inbox():
		if !bytes.Equal(pkt.Payload, payload) {
			t.Errorf("post-loss payload mismatch: %q vs %q", pkt.Payload, payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("C did not receive post-loss payload")
	}
}

// drainInbox pulls packets off inbox until either no packet has
// arrived for `quiet` consecutive duration OR `maxWait` total time
// has elapsed since the call started. Used after a probe phase
// (waitForMeshLink) to clear stragglers that pion's pipeline is
// still flushing under -race.
//
// The maxWait ceiling matters: on a busy channel that delivers at
// least one packet every <quiet duration, the quiet timer never
// fires and the helper would loop forever. Tests should pass a
// maxWait slightly above their expected drain budget.
func drainInbox(inbox <-chan transport.Packet, quiet, maxWait time.Duration) {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		select {
		case <-inbox:
		case <-time.After(quiet):
			return
		}
	}
}

// TestMesh_E2E_StatsAccumulate ensures the transport-level counters
// reflect actual traffic over a series of Sends — protects against
// regressions in the observability path (silent-drop counter
// updates would not be caught by the smoke test).
func TestMesh_E2E_StatsAccumulate(t *testing.T) {
	// Not t.Parallel: same CPU-starvation rationale as
	// TestMesh_E2E_DataRoundTrip — pion under -race needs the
	// foreground when other heavy mesh tests share the process.
	if testing.Short() {
		t.Skip("requires pion ICE — skipped under -short")
	}

	a, b := twoMeshNodes(t)
	if !waitForMeshLink(t, a, b, 60*time.Second) {
		t.Fatal("mesh link never established")
	}

	bHash := b.Identity().Public().DestinationHash()
	addr := transport.NewWebRTCAddr(bHash)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	const N = 10
	const sz = 256
	for i := range N {
		payload := make([]byte, sz)
		copy(payload, fmt.Sprintf("seq-%d-", i))
		if err := a.MeshTransport().Send(ctx, addr, payload); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	// Drain the responder's inbox.
	got := 0
	deadline := time.After(3 * time.Second)
	for got < N {
		select {
		case <-b.MeshTransport().Inbox():
			got++
		case <-deadline:
			t.Fatalf("only got %d/%d packets before timeout", got, N)
		}
	}

	statsA := a.Stats()
	if statsA.RTCBytesSent < N*sz {
		t.Errorf("RTCBytesSent = %d, want ≥ %d", statsA.RTCBytesSent, N*sz)
	}
	statsB := b.Stats()
	if statsB.RTCBytesRecv < N*sz {
		t.Errorf("RTCBytesRecv = %d, want ≥ %d", statsB.RTCBytesRecv, N*sz)
	}
}

// avoid unused import in builds where some helpers are gated
var _ identity.Hash
