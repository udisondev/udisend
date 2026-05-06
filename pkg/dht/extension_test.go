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

// captureExt is a dht.Extension that records every frame it receives.
type captureExt struct {
	mu     sync.Mutex
	frames []capturedFrame
}

type capturedFrame struct {
	typ     byte
	payload []byte
}

func (c *captureExt) HandleDHTFrame(_ context.Context, _ transport.Packet, typ byte, payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cp := append([]byte(nil), payload...)
	c.frames = append(c.frames, capturedFrame{typ: typ, payload: cp})
}

func (c *captureExt) snapshot() []capturedFrame {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]capturedFrame, len(c.frames))
	copy(out, c.frames)

	return out
}

// TestNode_ExtensionReceivesEmbedderFrames verifies that frames whose
// outer wire-frame type is outside the DHT-owned range (0x01–0x0F) are
// dispatched to Config.Extension. Embedders own opcodes ≥ 0x10; Phase
// 12 moved MsgRelay (0x10) out of pkg/dht for exactly this contract.
func TestNode_ExtensionReceivesEmbedderFrames(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()

	idA, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	trA := hub.NewMemoryTransport()
	trB := hub.NewMemoryTransport()

	ext := &captureExt{}
	nodeB := dht.NewNode(idB, trB, nil, dht.Config{
		RequestTimeout: 500 * time.Millisecond,
		LookupTimeout:  2 * time.Second,
		Extension:      ext,
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var wg sync.WaitGroup
	wg.Go(func() {
		nodeB.Run(ctx)
	})

	t.Cleanup(func() {
		nodeB.Close()
		wg.Wait()
	})

	_ = idA

	const embedderType byte = 0x10
	body := []byte("hello-from-embedder")
	frame, err := wire.EncodeFrame(embedderType, body)
	if err != nil {
		t.Fatal(err)
	}

	if err := trA.Send(ctx, trB.LocalAddr(), frame); err != nil {
		t.Fatalf("send: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if frames := ext.snapshot(); len(frames) > 0 {
			f := frames[0]
			if f.typ != embedderType {
				t.Fatalf("typ = 0x%02x, want 0x%02x", f.typ, embedderType)
			}
			if string(f.payload) != string(body) {
				t.Fatalf("payload = %q, want %q", f.payload, body)
			}

			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("extension never received frame; got %v", ext.snapshot())
}
