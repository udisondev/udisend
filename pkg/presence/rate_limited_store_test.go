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

// signedRecordBlob produces a valid presence record (signed) and returns
// the marshalled bytes plus the publisher's destination hash.
func signedRecordBlob(t *testing.T, addr string) ([]byte, dht.NodeID) {
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

func srcAddr(ip string) net.Addr {
	return &net.UDPAddr{IP: net.ParseIP(ip), Port: 9000}
}

// TestRateLimitedStore_TrackedIPsBounded covers the cardinality cap: an
// attacker spraying 1M distinct source IPs (residential IPv6 /64
// rotation, XFF spoofing through a misconfigured proxy) MUST NOT grow
// the bookkeeping maps without bound. Records past the cap pass through
// to the inner store (with its own size cap) but no per-IP quota slot
// is allocated.
func TestRateLimitedStore_TrackedIPsBounded(t *testing.T) {
	t.Parallel()

	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 16
	store.MaxTrackedIPs = 4

	// Push records from five distinct IPs. Only the first four are
	// admitted to the per-IP map; the fifth still lands in the inner
	// store (tracking is a soft layer above the store) but doesn't grow
	// the map.
	for i := byte(0); i < 5; i++ {
		blob, key := signedRecordBlob(t, "203.0.113.10:9000")
		store.PutFromSource(key, blob, 60*time.Second, srcAddr("198.18.0."+string(rune('1'+i))))
	}
	if tracked := store.TrackedIPCount(); tracked != 4 {
		t.Errorf("tracked IPs = %d, want 4 (cap-bound)", tracked)
	}
}

func TestRateLimitedStore_RejectsAfterLimitFromSameSource(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 3

	src := srcAddr("203.0.113.7")
	for i := range 5 {
		blob, key := signedRecordBlob(t, "203.0.113.7:900"+string(rune('0'+i)))
		store.PutFromSource(key, blob, 60*time.Second, src)
	}
	if got := inner.Size(); got != 3 {
		t.Fatalf("inner store has %d entries, want 3 (limit cap)", got)
	}
}

// Regression for the BLOCKING from review iter 3: previously the
// limiter keyed on rec.Address (forgeable), so an attacker could pin a
// victim IP's quota by claiming it. Now keying on the real source IP
// makes records claiming the victim's address-but-from-attacker-IP
// land in the attacker's bucket, leaving the victim free.
func TestRateLimitedStore_DoesNotLetAttackerExhaustVictimsQuota(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 2

	attacker := srcAddr("198.18.0.1")
	victim := srcAddr("203.0.113.7")
	const victimAddr = "203.0.113.7:9000"

	// Attacker fakes 5 records that all CLAIM victim's address.
	for range 5 {
		blob, key := signedRecordBlob(t, victimAddr)
		store.PutFromSource(key, blob, 60*time.Second, attacker)
	}

	// Now the victim publishes its own record from its real IP.
	victimBlob, victimKey := signedRecordBlob(t, victimAddr)
	store.PutFromSource(victimKey, victimBlob, 60*time.Second, victim)

	if _, ok := inner.Get(victimKey); !ok {
		t.Fatal("victim's own record was rejected — its quota was eaten by the attacker")
	}
}

func TestRateLimitedStore_AllowsDistinctSources(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 2

	for _, ip := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		blob, key := signedRecordBlob(t, ip+":9000")
		store.PutFromSource(key, blob, 60*time.Second, srcAddr(ip))
	}
	if got := inner.Size(); got != 3 {
		t.Fatalf("inner store has %d entries, want 3 (one per IP)", got)
	}
}

func TestRateLimitedStore_DropsForgedRecord(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 16

	// Build a record then tamper with the address; verification fails
	// before the slot counter touches.
	blob, key := signedRecordBlob(t, "203.0.113.7:9000")
	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 0xFF // bit-flip in the signature

	store.PutFromSource(key, tampered, 60*time.Second, srcAddr("203.0.113.7"))
	if _, ok := inner.Get(key); ok {
		t.Fatal("tampered record was accepted")
	}
}

func TestRateLimitedStore_PassesNonPresenceThrough(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 1

	var key dht.NodeID
	store.PutFromSource(key, []byte("not a record"), 60*time.Second, srcAddr("10.0.0.1"))
	if _, ok := inner.Get(key); !ok {
		t.Fatal("non-presence blob was rate-limited")
	}
}

func TestRateLimitedStore_SweepPrunesExpiredSlots(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = 2

	src := srcAddr("10.0.0.1")
	// Two short-lived records take both slots.
	for range 2 {
		blob, key := signedRecordBlob(t, "10.0.0.1:9000")
		store.PutFromSource(key, blob, 1*time.Nanosecond, src)
	}

	// Sweep at a future time — both should evict from the inner store
	// AND the rate-limit bookkeeping must release the slots.
	store.Sweep(time.Now().Add(time.Second))

	// A third write from the same IP must succeed now that slots are free.
	blob, key := signedRecordBlob(t, "10.0.0.1:9000")
	store.PutFromSource(key, blob, 60*time.Second, src)
	if _, ok := inner.Get(key); !ok {
		t.Fatal("post-sweep write was rejected — Sweep did not release per-IP slots")
	}
}

func TestRateLimitedStore_NegativeDisables(t *testing.T) {
	t.Parallel()
	inner := dht.NewMemoryStore(nil)
	store := presence.NewRateLimitedStore(inner)
	store.MaxPerIP = -1

	src := srcAddr("10.0.0.1")
	for range 100 {
		blob, key := signedRecordBlob(t, "10.0.0.1:9000")
		store.PutFromSource(key, blob, 60*time.Second, src)
	}
	if got := inner.Size(); got != 100 {
		t.Fatalf("expected all 100 stored when limiting disabled, got %d", got)
	}
}
