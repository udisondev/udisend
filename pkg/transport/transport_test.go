package transport_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/transport"
)

func TestMemory_Roundtrip(t *testing.T) {
	t.Parallel()
	hub := transport.NewMemoryHub()
	a := hub.NewMemoryTransport()
	b := hub.NewMemoryTransport()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := a.Send(ctx, b.LocalAddr(), []byte("ping")); err != nil {
		t.Fatal(err)
	}
	select {
	case pkt := <-b.Inbox():
		if string(pkt.Payload) != "ping" {
			t.Fatalf("got %q", pkt.Payload)
		}
		if pkt.From.String() != a.LocalAddr().String() {
			t.Fatalf("from = %v, want %v", pkt.From, a.LocalAddr())
		}
	case <-time.After(time.Second):
		t.Fatal("no packet received")
	}
}

func TestMemory_UnknownPeer(t *testing.T) {
	t.Parallel()
	hub := transport.NewMemoryHub()
	a := hub.NewMemoryTransport()
	t.Cleanup(func() { _ = a.Close() })

	gone, err := transport.ParseMemoryAddr("mem:does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(t.Context(), gone, []byte("x")); err == nil {
		t.Fatal("expected error for unknown peer")
	}
}

func TestMemory_CloseStopsSend(t *testing.T) {
	t.Parallel()
	hub := transport.NewMemoryHub()
	a := hub.NewMemoryTransport()
	b := hub.NewMemoryTransport()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	err := a.Send(t.Context(), b.LocalAddr(), []byte("x"))
	if !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	_ = b.Close()
}

func TestMemory_Concurrent(t *testing.T) {
	t.Parallel()
	hub := transport.NewMemoryHub()
	a := hub.NewMemoryTransport()
	b := hub.NewMemoryTransport()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })

	const n = 100
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		count := 0
		for range b.Inbox() {
			count++
			if count == n {
				return
			}
		}
	}()
	for i := range n {
		_ = a.Send(t.Context(), b.LocalAddr(), []byte{byte(i)})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive all packets")
	}
}

func TestUDP_Roundtrip(t *testing.T) {
	t.Parallel()
	a, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Skipf("UDP not available: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := a.Send(ctx, b.LocalAddr(), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case pkt := <-b.Inbox():
		if string(pkt.Payload) != "hello" {
			t.Fatalf("got %q", pkt.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no packet received")
	}
}

func TestUDP_PayloadTooLarge(t *testing.T) {
	t.Parallel()
	a, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	huge := make([]byte, transport.MaxPacketSize+1)
	if err := a.Send(t.Context(), a.LocalAddr(), huge); err == nil {
		t.Fatal("expected error for oversized payload")
	}
}
