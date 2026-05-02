package transport_test

import (
	"context"
	"testing"

	"github.com/udisondev/udisend/pkg/transport"
)

// BenchmarkMemorySend exercises the in-process Send → forward → Inbox path
// on a single packet. We drain on the receiver side inside the loop so the
// channel does not fill up.
func BenchmarkMemorySend(b *testing.B) {
	hub := transport.NewMemoryHub()
	a := hub.NewMemoryTransport()
	rcv := hub.NewMemoryTransport()
	b.Cleanup(func() { _ = a.Close(); _ = rcv.Close() })

	payload := make([]byte, 256)
	ctx := context.Background()
	dst := rcv.LocalAddr()
	inbox := rcv.Inbox()

	// Drain inbox concurrently to keep the pipeline flowing.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-inbox:
			}
		}
	}()
	b.Cleanup(func() { close(done) })

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		if err := a.Send(ctx, dst, payload); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMemorySendLarger uses a typical signaling envelope size.
func BenchmarkMemorySend_1KB(b *testing.B) {
	hub := transport.NewMemoryHub()
	a := hub.NewMemoryTransport()
	rcv := hub.NewMemoryTransport()
	b.Cleanup(func() { _ = a.Close(); _ = rcv.Close() })

	payload := make([]byte, 1024)
	ctx := context.Background()
	dst := rcv.LocalAddr()
	inbox := rcv.Inbox()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-inbox:
			}
		}
	}()
	b.Cleanup(func() { close(done) })

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		if err := a.Send(ctx, dst, payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseMemoryAddr(b *testing.B) {
	addr := "mem:42"
	b.ReportAllocs()
	for b.Loop() {
		out, err := transport.ParseMemoryAddr(addr)
		if err != nil {
			b.Fatal(err)
		}
		_ = out
	}
}
