package presence_test

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
)

func mkSignedRecord(t *testing.T, addr string) ([]byte, dht.NodeID) {
	t.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := &presence.Record{
		Address:  addr,
		IssuedAt: time.Now().UTC(),
	}
	if err := r.Sign(id); err != nil {
		t.Fatal(err)
	}
	blob, err := r.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return blob, id.Public().DestinationHash()
}

func TestRateLimitedStore_RejectsAfterLimit(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 3

	const ip = "203.0.113.7"
	for i := 0; i < 5; i++ {
		blob, key := mkSignedRecord(t, ip+":900"+string(rune('0'+i)))
		store.Put(key, blob, 60*time.Second)
	}
	if got := inner.Size(); got != 3 {
		t.Fatalf("inner store has %d entries, want 3 (limit cap)", got)
	}
}

func TestRateLimitedStore_AllowsDistinctIPs(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 2

	for _, ip := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		blob, key := mkSignedRecord(t, ip+":9000")
		store.Put(key, blob, 60*time.Second)
	}
	if got := inner.Size(); got != 3 {
		t.Fatalf("inner store has %d entries, want 3 (one per IP)", got)
	}
}

func TestRateLimitedStore_PassesNonPresenceThrough(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 1

	var key dht.NodeID
	store.Put(key, []byte("not a record"), 60*time.Second)
	if _, ok := inner.Get(key); !ok {
		t.Fatal("non-presence blob was rate-limited")
	}
}

func TestRateLimitedStore_NegativeDisables(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = -1

	for i := 0; i < 100; i++ {
		blob, key := mkSignedRecord(t, "10.0.0.1:9000")
		store.Put(key, blob, 60*time.Second)
	}
	if got := inner.Size(); got != 100 {
		t.Fatalf("expected all 100 stored when limiting disabled, got %d", got)
	}
}
