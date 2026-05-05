package webrtc_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	udwebrtc "github.com/udisondev/udisend/pkg/webrtc"
)

// fakeMeshTransport is an in-memory stand-in for *transport.WebRTCTransport
// that lets tests script Connect outcomes without spinning up real pion.
type fakeMeshTransport struct {
	mu sync.Mutex

	connected map[identity.Hash]bool

	// connectFn allows test cases to script outcomes per call.
	// Defaults to "always succeed".
	connectFn func(peer identity.Hash) error

	connectAttempts atomic.Int64
}

func newFakeMesh() *fakeMeshTransport {
	return &fakeMeshTransport{connected: make(map[identity.Hash]bool)}
}

func (f *fakeMeshTransport) Connect(_ context.Context, peer identity.Hash) error {
	f.connectAttempts.Add(1)
	f.mu.Lock()
	fn := f.connectFn
	f.mu.Unlock()
	if fn != nil {
		if err := fn(peer); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.connected[peer] = true
	f.mu.Unlock()

	return nil
}

func (f *fakeMeshTransport) IsConnected(peer identity.Hash) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.connected[peer]
}

func (f *fakeMeshTransport) Peers() []identity.Hash {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]identity.Hash, 0, len(f.connected))
	for h := range f.connected {
		out = append(out, h)
	}

	return out
}

func (f *fakeMeshTransport) Disconnect(peer identity.Hash) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected[peer] {
		return false
	}
	delete(f.connected, peer)

	return true
}

// drop simulates a transport-side drop, used to test the manager's
// reconcile-detects-drop path.
func (f *fakeMeshTransport) drop(peer identity.Hash) {
	f.mu.Lock()
	delete(f.connected, peer)
	f.mu.Unlock()
}

// setConnectFn installs a callback invoked on every Connect attempt.
// Returning a non-nil error makes Connect fail.
func (f *fakeMeshTransport) setConnectFn(fn func(identity.Hash) error) {
	f.mu.Lock()
	f.connectFn = fn
	f.mu.Unlock()
}

func mkHash(b byte) identity.Hash {
	var h identity.Hash
	h[0] = b
	return h
}

// TestPeerManager_FillsToK verifies that the manager dials up to its
// MaxLinks target on the first reconcile pass.
func TestPeerManager_FillsToK(t *testing.T) {
	t.Parallel()

	mt := newFakeMesh()
	candidates := []identity.Hash{
		mkHash(0x01), mkHash(0x02), mkHash(0x03), mkHash(0x04),
		mkHash(0x05), mkHash(0x06), mkHash(0x07), mkHash(0x08),
		mkHash(0x09), mkHash(0x0a),
	}

	pm, err := udwebrtc.NewPeerManager(udwebrtc.PeerManagerConfig{
		Transport: mt,
		Selector: udwebrtc.PeerSelectorFunc(func(_ context.Context, _ int) []identity.Hash {
			return candidates
		}),
		MaxLinks:       4,
		TickInterval:   time.Hour, // never fire — we only want first reconcile
		BackoffInitial: 10 * time.Millisecond,
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewPeerManager: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- pm.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(mt.Peers()) >= 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := len(mt.Peers())
	if got != 4 {
		t.Errorf("connected peers = %d, want 4", got)
	}

	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Run: %v", err)
	}
}

// TestPeerManager_BackoffOnFailure ensures repeated Connect failures
// space out attempts via exponential backoff rather than spinning at
// the tick rate. We do not require an exact schedule (jitter), but
// the second attempt must NOT happen in the first BackoffInitial/2
// window, and the manager must continue retrying eventually.
func TestPeerManager_BackoffOnFailure(t *testing.T) {
	t.Parallel()

	mt := newFakeMesh()
	mt.setConnectFn(func(_ identity.Hash) error {
		return errors.New("boom")
	})

	target := mkHash(0xaa)
	pm, err := udwebrtc.NewPeerManager(udwebrtc.PeerManagerConfig{
		Transport: mt,
		Selector: udwebrtc.PeerSelectorFunc(func(_ context.Context, _ int) []identity.Hash {
			return []identity.Hash{target}
		}),
		MaxLinks:       1,
		TickInterval:   50 * time.Millisecond, // tick fast so reconcile runs often
		BackoffInitial: 200 * time.Millisecond,
		BackoffMax:     time.Second,
		BackoffJitter:  0.0, // deterministic for testing
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewPeerManager: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pm.Run(ctx) }()

	// First attempt fires immediately on initial reconcile.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if mt.connectAttempts.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if mt.connectAttempts.Load() < 1 {
		t.Fatalf("first attempt did not fire; got %d", mt.connectAttempts.Load())
	}

	// Within BackoffInitial/2 ticks (≤100ms), no second attempt yet.
	time.Sleep(100 * time.Millisecond)
	if got := mt.connectAttempts.Load(); got > 1 {
		t.Errorf("second attempt fired too early: got %d after 100ms", got)
	}

	// After more than BackoffInitial * 1, a second attempt fires.
	time.Sleep(400 * time.Millisecond)
	if got := mt.connectAttempts.Load(); got < 2 {
		t.Errorf("second attempt never fired: got %d after 500ms total", got)
	}

	cancel()
	<-done
}

// TestPeerManager_DetectsDropAndReconnects verifies the path where
// the transport drops a peer outside our control (DC failure / ICE
// timeout) — the next reconcile flips state and re-dials.
func TestPeerManager_DetectsDropAndReconnects(t *testing.T) {
	t.Parallel()

	mt := newFakeMesh()
	target := mkHash(0xbb)

	pm, err := udwebrtc.NewPeerManager(udwebrtc.PeerManagerConfig{
		Transport: mt,
		Selector: udwebrtc.PeerSelectorFunc(func(_ context.Context, _ int) []identity.Hash {
			return []identity.Hash{target}
		}),
		MaxLinks:       1,
		TickInterval:   30 * time.Millisecond,
		BackoffInitial: 5 * time.Millisecond,
		BackoffMax:     50 * time.Millisecond,
		BackoffJitter:  0.0,
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewPeerManager: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pm.Run(ctx) }()

	// Wait for initial Connect.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if mt.IsConnected(target) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mt.IsConnected(target) {
		t.Fatal("initial connect never landed")
	}
	first := mt.connectAttempts.Load()

	// Drop the peer — manager must re-dial on its next reconcile.
	mt.drop(target)

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if mt.IsConnected(target) && mt.connectAttempts.Load() > first {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mt.IsConnected(target) {
		t.Fatal("manager did not reconnect after drop")
	}
	if mt.connectAttempts.Load() <= first {
		t.Errorf("connect-after-drop: attempts=%d, want > %d", mt.connectAttempts.Load(), first)
	}

	cancel()
	<-done
}

// TestPeerManager_DropsEvictedPeer verifies the path where the
// selector removes a previously-tracked peer: the manager
// disconnects it and forgets the entry.
func TestPeerManager_DropsEvictedPeer(t *testing.T) {
	t.Parallel()

	mt := newFakeMesh()
	keep := mkHash(0xcc)
	evict := mkHash(0xdd)

	var (
		mu       sync.Mutex
		peerSet  []identity.Hash
	)
	peerSet = []identity.Hash{keep, evict}

	pm, err := udwebrtc.NewPeerManager(udwebrtc.PeerManagerConfig{
		Transport: mt,
		Selector: udwebrtc.PeerSelectorFunc(func(_ context.Context, _ int) []identity.Hash {
			mu.Lock()
			defer mu.Unlock()
			out := make([]identity.Hash, len(peerSet))
			copy(out, peerSet)
			return out
		}),
		MaxLinks:       8,
		TickInterval:   25 * time.Millisecond,
		BackoffInitial: 5 * time.Millisecond,
		BackoffMax:     50 * time.Millisecond,
		BackoffJitter:  0.0,
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewPeerManager: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pm.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if mt.IsConnected(keep) && mt.IsConnected(evict) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !(mt.IsConnected(keep) && mt.IsConnected(evict)) {
		t.Fatal("two peers never connected")
	}

	// Selector now drops `evict`. Manager must disconnect it.
	mu.Lock()
	peerSet = []identity.Hash{keep}
	mu.Unlock()
	pm.PokeForReconcile()

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !mt.IsConnected(evict) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if mt.IsConnected(evict) {
		t.Error("evicted peer is still connected")
	}
	if !mt.IsConnected(keep) {
		t.Error("retained peer was disconnected by mistake")
	}

	cancel()
	<-done
}

// TestNewPeerManager_RejectsBadConfig covers the constructor's
// validation of required dependencies.
func TestNewPeerManager_RejectsBadConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  udwebrtc.PeerManagerConfig
	}{
		{name: "no transport", cfg: udwebrtc.PeerManagerConfig{Selector: udwebrtc.PeerSelectorFunc(func(context.Context, int) []identity.Hash { return nil })}},
		{name: "no selector", cfg: udwebrtc.PeerManagerConfig{Transport: newFakeMesh()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := udwebrtc.NewPeerManager(tt.cfg)
			if err == nil {
				t.Errorf("NewPeerManager = nil err, want error for %s", tt.name)
			}
		})
	}
}
