package presence_test

import (
	"crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
)

func benchSignedRecord(b *testing.B) (*presence.Record, *identity.Identity) {
	b.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	r := &presence.Record{
		Address:      "203.0.113.10:43901",
		Capabilities: presence.CapPublicIP | presence.CapCanRelay,
		IssuedAt:     time.Now().UTC(),
		UptimeHint:   12345,
	}
	if err := r.Sign(id); err != nil {
		b.Fatal(err)
	}
	return r, id
}

func BenchmarkRecord_Marshal(b *testing.B) {
	r, _ := benchSignedRecord(b)
	b.ReportAllocs()
	for b.Loop() {
		blob, err := r.MarshalBinary()
		if err != nil {
			b.Fatal(err)
		}
		_ = blob
	}
}

func BenchmarkRecord_Unmarshal(b *testing.B) {
	r, _ := benchSignedRecord(b)
	blob, err := r.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		var out presence.Record
		if err := out.UnmarshalBinary(blob); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecord_Verify(b *testing.B) {
	r, _ := benchSignedRecord(b)
	now := time.Now().UTC()
	b.ReportAllocs()
	for b.Loop() {
		if err := r.Verify(now, time.Hour); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRateLimitedStore_PutFromSource is the DHT inbound STORE path on
// a network node. Same source IP each call (steady-state update of one
// record).
func BenchmarkRateLimitedStore_PutFromSource(b *testing.B) {
	r, _ := benchSignedRecord(b)
	blob, err := r.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	src := &net.UDPAddr{IP: net.ParseIP("203.0.113.10"), Port: 43901}
	store := presence.NewRateLimitedStore(dht.NewMemoryStore(nil))
	key := dht.NodeID(r.DestinationHash())
	b.ReportAllocs()
	for b.Loop() {
		store.PutFromSource(key, blob, time.Minute, src)
	}
}

// Same as above but with many distinct (key, ip) pairs — exercises the
// per-IP map insertion and resize behaviour.
func BenchmarkRateLimitedStore_PutFromSource_ManyIPs(b *testing.B) {
	r, _ := benchSignedRecord(b)
	blob, err := r.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	store := presence.NewRateLimitedStore(dht.NewMemoryStore(nil))
	store.MaxPerIP = -1 // disable limit so we exercise the fast pass-through path
	key := dht.NodeID(r.DestinationHash())
	addrs := make([]*net.UDPAddr, 64)
	for i := range addrs {
		addrs[i] = &net.UDPAddr{IP: net.IPv4(192, 0, 2, byte(i+1)), Port: 9000}
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		store.PutFromSource(key, blob, time.Minute, addrs[i%len(addrs)])
		i++
	}
}
