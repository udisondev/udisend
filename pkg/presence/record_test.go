package presence_test

import (
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
)

func mustID(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRecord_SignVerify(t *testing.T) {
	t.Parallel()
	id := mustID(t)
	r := presence.Record{
		Address:      "127.0.0.1:9000",
		Capabilities: presence.CapPublicIP | presence.CapCanRelay,
		IssuedAt:     time.Now().UTC(),
	}
	if err := r.Sign(id); err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(time.Now().UTC(), time.Minute); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r.DestinationHash() != id.Public().DestinationHash() {
		t.Fatalf("dest hash mismatch")
	}
}

func TestRecord_BadSignature(t *testing.T) {
	t.Parallel()
	id := mustID(t)
	r := presence.Record{Address: "127.0.0.1:1", IssuedAt: time.Now()}
	_ = r.Sign(id)
	r.Signature[0] ^= 0xFF
	if err := r.Verify(time.Now(), time.Minute); !errors.Is(err, presence.ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

func TestRecord_TamperedAddress(t *testing.T) {
	t.Parallel()
	id := mustID(t)
	r := presence.Record{Address: "127.0.0.1:1", IssuedAt: time.Now()}
	_ = r.Sign(id)
	r.Address = "evil:6666" // post-sign tampering
	if err := r.Verify(time.Now(), time.Minute); !errors.Is(err, presence.ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

func TestRecord_Expired(t *testing.T) {
	t.Parallel()
	id := mustID(t)
	r := presence.Record{
		Address:  "127.0.0.1:1",
		IssuedAt: time.Now().Add(-2 * time.Minute),
	}
	_ = r.Sign(id)
	err := r.Verify(time.Now(), time.Minute)
	if !errors.Is(err, presence.ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}

func TestRecord_MarshalRoundtrip(t *testing.T) {
	t.Parallel()
	id := mustID(t)
	r := presence.Record{
		Address:      "127.0.0.1:1234",
		Capabilities: presence.CapCanSTUN | presence.CapCanTURN,
		IssuedAt:     time.Now().Truncate(time.Second).UTC(),
	}
	if err := r.Sign(id); err != nil {
		t.Fatal(err)
	}
	blob, err := r.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var got presence.Record
	if err := got.UnmarshalBinary(blob); err != nil {
		t.Fatal(err)
	}
	if got.Address != r.Address {
		t.Fatal("address")
	}
	if got.Capabilities != r.Capabilities {
		t.Fatal("caps")
	}
	if !got.IssuedAt.Equal(r.IssuedAt) {
		t.Fatalf("issued: %v vs %v", got.IssuedAt, r.IssuedAt)
	}
	if err := got.Verify(time.Now(), time.Hour); err != nil {
		t.Fatalf("verify after roundtrip: %v", err)
	}
}

func TestCapability_String(t *testing.T) {
	t.Parallel()
	if presence.Capability(0).String() != "none" {
		t.Fatal("zero cap")
	}
	caps := presence.CapPublicIP | presence.CapCanTURN
	if got := caps.String(); got == "" {
		t.Fatal("empty string for non-zero caps")
	}
}

func TestCache_PutGetSweep(t *testing.T) {
	t.Parallel()
	id := mustID(t)
	cache := presence.NewCache(time.Minute)
	now := time.Now().UTC()
	r := presence.Record{
		Address:  "127.0.0.1:1",
		IssuedAt: now,
	}
	_ = r.Sign(id)
	ok, err := cache.Put(now, &r)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Put returned false for fresh record")
	}
	got, ok := cache.Get(now, r.DestinationHash())
	if !ok {
		t.Fatal("Get returned !ok immediately after Put")
	}
	if got.Address != "127.0.0.1:1" {
		t.Fatal("Get returned different value")
	}

	// Older record is rejected.
	older := r
	older.IssuedAt = now.Add(-time.Second)
	_ = older.Sign(id)
	ok, _ = cache.Put(now, &older)
	if ok {
		t.Fatal("older record should be rejected")
	}

	// Sweep removes expired entries.
	future := now.Add(2 * time.Minute)
	cache.Sweep(future)
	if cache.Size() != 0 {
		t.Fatalf("after sweep: size = %d", cache.Size())
	}
}

func FuzzRecordUnmarshal(f *testing.F) {
	id, _ := identity.Generate(rand.Reader)
	r := presence.Record{Address: "127.0.0.1:1", IssuedAt: time.Now().UTC()}
	_ = r.Sign(id)
	if blob, err := r.MarshalBinary(); err == nil {
		f.Add(blob)
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var got presence.Record
		_ = got.UnmarshalBinary(data)
	})
}
