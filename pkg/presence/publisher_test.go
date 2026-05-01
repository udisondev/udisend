package presence_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
)

type fakeDHT struct {
	mu     sync.Mutex
	values map[dht.NodeID][]byte
}

func (f *fakeDHT) PutValue(_ context.Context, key dht.NodeID, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.values == nil {
		f.values = make(map[dht.NodeID][]byte)
	}
	c := make([]byte, len(value))
	copy(c, value)
	f.values[key] = c
	return nil
}

func (f *fakeDHT) LookupValue(_ context.Context, key dht.NodeID) ([]byte, []dht.Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[key]
	if !ok {
		return nil, nil, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil, nil
}

func TestPublisher_PublishesAndResolverReads(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)
	dhtFake := &fakeDHT{}

	pub := presence.NewPublisher(presence.PublisherConfig{
		Identity:     id,
		Address:      "127.0.0.1:9999",
		Capabilities: presence.CapPublicIP,
		TTL:          time.Minute,
		Refresh:      10 * time.Millisecond,
		DHT:          dhtFake,
	})

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	go pub.Run(ctx)
	// Wait until publish has happened.
	deadline := time.Now().Add(2 * time.Second)
	for {
		dhtFake.mu.Lock()
		_, ok := dhtFake.values[id.Public().DestinationHash()]
		dhtFake.mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("publisher never wrote to DHT")
		}
		time.Sleep(5 * time.Millisecond)
	}

	resolver := presence.NewResolver(dhtFake, time.Minute)
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	rec, err := resolver.Lookup(ctx2, id.Public().DestinationHash())
	if err != nil {
		t.Fatalf("resolver lookup: %v", err)
	}
	if rec.Address != "127.0.0.1:9999" {
		t.Fatalf("address = %q", rec.Address)
	}
	if !rec.Capabilities.Has(presence.CapPublicIP) {
		t.Fatal("missing PublicIP capability")
	}
}

func TestResolver_RejectsForgedRecord(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)
	imposter, _ := identity.Generate(rand.Reader)

	r := presence.Record{
		Address:  "evil:6666",
		IssuedAt: time.Now().UTC(),
	}
	// imposter signs a record claiming to be id (signature fails because
	// Sign overwrites Public with imposter, but a malicious peer could
	// also publish under id's key — emulate that).
	_ = r.Sign(imposter)
	r.Public = id.Public() // forge: now pubkey says "id" but signature was by imposter
	blob, _ := r.MarshalBinary()

	dhtFake := &fakeDHT{values: map[dht.NodeID][]byte{
		id.Public().DestinationHash(): blob,
	}}
	resolver := presence.NewResolver(dhtFake, time.Minute)
	if _, err := resolver.Lookup(t.Context(), id.Public().DestinationHash()); !errors.Is(err, presence.ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

func TestResolver_NotFound(t *testing.T) {
	t.Parallel()
	dhtFake := &fakeDHT{}
	resolver := presence.NewResolver(dhtFake, time.Minute)
	id, _ := identity.Generate(rand.Reader)
	_, err := resolver.Lookup(t.Context(), id.Public().DestinationHash())
	if !errors.Is(err, presence.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
