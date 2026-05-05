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

// BenchmarkUDPRecv_AllocPerPacket measures the per-delivered-packet
// allocation count in the UDP recv loop. The hot path is the only
// place under sustained DoS where allocator pressure becomes
// observable: 100k pps × make([]byte, n) per packet was the audit's
// concern. Keep the payload small (typical signaling/DHT frame) so
// the benchmark stresses framing overhead, not byte-throughput.
func BenchmarkUDPRecv_AllocPerPacket(b *testing.B) {
	a, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	r, err := transport.ListenUDP("127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = a.Close(); _ = r.Close() })

	payload := make([]byte, 256)
	ctx := context.Background()
	dst := r.LocalAddr()
	inbox := r.Inbox()

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for b.Loop() {
		if err := a.Send(ctx, dst, payload); err != nil {
			b.Fatal(err)
		}
		pkt := <-inbox
		pkt.Release()
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
