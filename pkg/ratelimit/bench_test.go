package ratelimit_test

import (
	"strconv"
	"testing"

	"github.com/udisondev/udisend/pkg/ratelimit"
)

// BenchmarkLimiter_Allow_HotKey simulates the steady-state hot path: one
// IP banging the network node, bucket already exists.
func BenchmarkLimiter_Allow_HotKey(b *testing.B) {
	l := ratelimit.New(1_000_000, 1_000_000) // effectively unlimited for the bench
	// Warm up the bucket entry so we measure the steady-state path.
	l.Allow("203.0.113.10")
	b.ReportAllocs()
	for b.Loop() {
		l.Allow("203.0.113.10")
	}
}

// BenchmarkLimiter_Allow_ManyKeys exercises the map-lookup path with a
// large warm working set (no eviction during the bench).
func BenchmarkLimiter_Allow_ManyKeys(b *testing.B) {
	const keys = 1024
	keyset := make([]string, keys)
	for i := range keyset {
		keyset[i] = "203.0.113." + strconv.Itoa(i)
	}
	l := ratelimit.New(1_000_000, 1_000_000)
	for _, k := range keyset {
		l.Allow(k)
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		l.Allow(keyset[i&(keys-1)])
		i++
	}
}

// BenchmarkLimiter_Allow_Disabled is the "InboundRate=0" fast path used by
// nodes that opt out of rate limiting.
func BenchmarkLimiter_Allow_Disabled(b *testing.B) {
	l := ratelimit.New(0, 0)
	b.ReportAllocs()
	for b.Loop() {
		l.Allow("anything")
	}
}
