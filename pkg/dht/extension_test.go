package dht_test

import (
	"context"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
	"github.com/udisondev/udisend/pkg/wire"
)

// captureExt is a dht.Extension that records every frame it receives
// and signals on a channel so tests can wait deterministically.
type captureExt struct {
	mu       sync.Mutex
	frames   []capturedFrame
	notifyCh chan struct{}
}

type capturedFrame struct {
	typ     byte
	payload []byte
}

func newCaptureExt() *captureExt {
	return &captureExt{notifyCh: make(chan struct{}, 16)}
}

func (c *captureExt) HandleDHTFrame(_ context.Context, _ transport.Packet, typ byte, payload []byte) {
	cp := append([]byte(nil), payload...)
	c.mu.Lock()
	c.frames = append(c.frames, capturedFrame{typ: typ, payload: cp})
	c.mu.Unlock()

	select {
	case c.notifyCh <- struct{}{}:
	default:
	}
}

func (c *captureExt) snapshot() []capturedFrame {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]capturedFrame, len(c.frames))
	copy(out, c.frames)

	return out
}

// extensionFixture spins up a single dht.Node with a captureExt
// attached and a peer transport ready to send into it.
type extensionFixture struct {
	ctx    context.Context
	cancel context.CancelFunc
	hub    *transport.MemoryHub
	trA    transport.Transport
	trB    transport.Transport
	ext    *captureExt
	wg     sync.WaitGroup
}

func newExtensionFixture(t *testing.T) *extensionFixture {
	t.Helper()

	hub := transport.NewMemoryHub()
	idB, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trA := hub.NewMemoryTransport()
	trB := hub.NewMemoryTransport()

	ext := newCaptureExt()
	nodeB := dht.NewNode(idB, trB, nil, dht.Config{
		RequestTimeout: 500 * time.Millisecond,
		LookupTimeout:  2 * time.Second,
		Extension:      ext,
	})

	ctx, cancel := context.WithCancel(t.Context())

	f := &extensionFixture{
		ctx:    ctx,
		cancel: cancel,
		hub:    hub,
		trA:    trA,
		trB:    trB,
		ext:    ext,
	}
	f.wg.Go(func() {
		nodeB.Run(ctx)
	})
	t.Cleanup(func() {
		nodeB.Close()
		cancel()
		f.wg.Wait()
	})

	return f
}

// TestNode_ExtensionReceivesEmbedderFrames verifies that frames whose
// outer wire-frame type sits in the embedder range (>= ExtensionRangeMin)
// are dispatched to Config.Extension. Embedders own these opcodes;
// Phase 12 moved relay (0x10) out of pkg/dht for exactly this contract.
func TestNode_ExtensionReceivesEmbedderFrames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		typ  byte
	}{
		{name: "first embedder byte", typ: dht.ExtensionRangeMin},
		{name: "arbitrary embedder kind", typ: 0x42},
		{name: "max embedder byte", typ: 0xff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newExtensionFixture(t)

			body := []byte("hello-from-embedder")
			frame, err := wire.EncodeFrame(tt.typ, body)
			if err != nil {
				t.Fatal(err)
			}

			if err := f.trA.Send(f.ctx, f.trB.LocalAddr(), frame); err != nil {
				t.Fatalf("send: %v", err)
			}

			select {
			case <-f.ext.notifyCh:
			case <-time.After(2 * time.Second):
				t.Fatalf("extension never received frame; got %v", f.ext.snapshot())
			}

			frames := f.ext.snapshot()
			if len(frames) != 1 {
				t.Fatalf("len(frames) = %d, want 1", len(frames))
			}
			if frames[0].typ != tt.typ {
				t.Errorf("typ = 0x%02x, want 0x%02x", frames[0].typ, tt.typ)
			}
			if string(frames[0].payload) != string(body) {
				t.Errorf("payload = %q, want %q", frames[0].payload, body)
			}
		})
	}
}

// TestNode_ReservedRangeDropped verifies that opcodes in the reserved
// DHT-future-use range (0x09–0x0F) are dropped at the inbound boundary
// — they neither reach Extension nor crash the node. Embedders that
// pick these bytes today would be silently broken when pkg/dht claims
// them; the drop makes the contract enforced, not just documented.
func TestNode_ReservedRangeDropped(t *testing.T) {
	t.Parallel()

	tests := []byte{0x09, 0x0a, 0x0c, 0x0f}

	for _, typ := range tests {
		t.Run("reserved 0x"+hex2(typ), func(t *testing.T) {
			t.Parallel()

			f := newExtensionFixture(t)

			body := []byte("would-be-future-dht-opcode")
			frame, err := wire.EncodeFrame(typ, body)
			if err != nil {
				t.Fatal(err)
			}

			if err := f.trA.Send(f.ctx, f.trB.LocalAddr(), frame); err != nil {
				t.Fatalf("send: %v", err)
			}

			// We must wait long enough that delivery would have
			// happened — but no signal is the success condition.
			// 250 ms is generous on a MemoryHub.
			select {
			case <-f.ext.notifyCh:
				t.Fatalf("reserved opcode 0x%02x reached extension; got %v",
					typ, f.ext.snapshot())
			case <-time.After(250 * time.Millisecond):
			}
		})
	}
}

func hex2(b byte) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[b>>4], hex[b&0x0f]})
}
