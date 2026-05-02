package network

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
)

// stubBackend lets each test pin Closest / LookupNode behaviour without
// spinning a real dht.Node.
type stubBackend struct {
	closest    []dht.Contact
	lookup     []dht.Contact
	lookupErr  error
	lookupCtx  context.Context
	lookupHash identity.Hash
	lookupHits int
}

func (s *stubBackend) Closest(_ identity.Hash, _ int) []dht.Contact {
	return s.closest
}

func (s *stubBackend) LookupNode(ctx context.Context, target identity.Hash) ([]dht.Contact, error) {
	s.lookupCtx = ctx
	s.lookupHash = target
	s.lookupHits++
	return s.lookup, s.lookupErr
}

func mkAddr(t *testing.T, s string) net.Addr {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", s)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func mkContact(t *testing.T, host string) dht.Contact {
	t.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return dht.Contact{ID: id.Public().DestinationHash(), Addr: mkAddr(t, host)}
}

// fixedContact pins both id and addr for explicit "this is the target" tests.
func fixedContact(target identity.Hash, addr net.Addr) dht.Contact {
	return dht.Contact{ID: target, Addr: addr}
}

func TestRouter_DirectMatchSkipsLookup(t *testing.T) {
	t.Parallel()
	target := mkContact(t, "127.0.0.1:1000").ID
	want := mkAddr(t, "127.0.0.1:1000")
	stub := &stubBackend{closest: []dht.Contact{fixedContact(target, want)}}
	r := dhtRouter{backend: stub, timeout: 100 * time.Millisecond}

	got, ok := r.NextHop(context.Background(), target)
	if !ok || got.String() != want.String() {
		t.Fatalf("NextHop = %v,%v; want %v,true", got, ok, want)
	}
	if stub.lookupHits != 0 {
		t.Fatalf("LookupNode called %d times for direct match (expected 0)", stub.lookupHits)
	}
}

func TestRouter_PathDiscoveryFindsTarget(t *testing.T) {
	t.Parallel()
	target := mkContact(t, "127.0.0.1:1234").ID
	other := mkContact(t, "127.0.0.1:1111")
	stub := &stubBackend{
		closest: []dht.Contact{other},
		lookup:  []dht.Contact{fixedContact(target, mkAddr(t, "127.0.0.1:1234"))},
	}
	r := dhtRouter{backend: stub, timeout: 100 * time.Millisecond}

	got, ok := r.NextHop(context.Background(), target)
	if !ok {
		t.Fatal("NextHop returned !ok despite lookup hit")
	}
	if got.String() != "127.0.0.1:1234" {
		t.Fatalf("expected target's addr, got %v", got)
	}
	if stub.lookupHits != 1 {
		t.Fatalf("LookupNode hits = %d, want 1", stub.lookupHits)
	}
}

func TestRouter_FallsBackToBestKnownHop(t *testing.T) {
	t.Parallel()
	target := mkContact(t, "127.0.0.1:9999").ID
	best := mkContact(t, "127.0.0.1:2222")
	stub := &stubBackend{
		closest:   []dht.Contact{best},
		lookupErr: errors.New("no path"),
	}
	r := dhtRouter{backend: stub, timeout: 100 * time.Millisecond}

	got, ok := r.NextHop(context.Background(), target)
	if !ok {
		t.Fatal("NextHop returned !ok despite local closest contact")
	}
	if got.String() != best.Addr.String() {
		t.Fatalf("expected fallback to %v, got %v", best.Addr, got)
	}
}

func TestRouter_EmptyTableNoLookup(t *testing.T) {
	t.Parallel()
	stub := &stubBackend{lookupErr: errors.New("nothing")}
	r := dhtRouter{backend: stub, timeout: 100 * time.Millisecond}

	target, _ := identity.Generate(rand.Reader)
	if _, ok := r.NextHop(context.Background(), target.Public().DestinationHash()); ok {
		t.Fatal("NextHop returned ok with empty table and failed lookup")
	}
}

func TestRouter_LookupTimeoutBounded(t *testing.T) {
	t.Parallel()
	other := mkContact(t, "127.0.0.1:3333")
	stub := &stubBackend{closest: []dht.Contact{other}}
	r := dhtRouter{backend: stub, timeout: 50 * time.Millisecond}

	target, _ := identity.Generate(rand.Reader)
	r.NextHop(context.Background(), target.Public().DestinationHash())
	if stub.lookupCtx == nil {
		t.Fatal("lookup context was not captured")
	}
	deadline, ok := stub.lookupCtx.Deadline()
	if !ok {
		t.Fatal("lookup context has no deadline")
	}
	if remaining := time.Until(deadline); remaining > 60*time.Millisecond {
		t.Fatalf("lookup deadline too generous: %v remaining (want <=60ms)", remaining)
	}
}
