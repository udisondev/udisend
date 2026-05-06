package identity_test

import (
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
)

// stubSuite02 is a NON-CRYPTOGRAPHIC stub of a hypothetical second
// crypto-suite. It exists solely to prove that the Phase 11 Suite
// abstraction lets a new Suite-id slot in **without modifying any
// consumer** (pkg/dht, pkg/presence, pkg/signaling, pkg/webrtc, etc.).
//
// The stub returns deterministic-but-fake signatures and shared
// secrets. It is registered under id 0xFE so it cannot collide with
// the real Suite0x01 (id 0x01) or any future production suite that
// adheres to the convention of low-numbered ids for shipped suites.
//
// DO NOT USE IN PRODUCTION. The signatures are forgeable, the keys
// are predictable, and the ECDH output is a constant-derived value.

const stubSuiteID byte = 0xFE

type stubSuite struct{}

func (stubSuite) ID() byte { return stubSuiteID }

func (s stubSuite) Generate(r io.Reader) (identity.Local, error) {
	var seed [32]byte
	if _, err := io.ReadFull(r, seed[:]); err != nil {
		return nil, err
	}

	return &stubLocal{seed: seed}, nil
}

func (s stubSuite) ParseRemote(blob []byte) (identity.Remote, error) {
	if len(blob) == 0 || blob[0] != stubSuiteID {
		return nil, errors.New("stub: bad version byte")
	}
	if len(blob) != 1+32 {
		return nil, errors.New("stub: bad blob length")
	}

	var pub [32]byte
	copy(pub[:], blob[1:])

	return stubRemote{pub: pub}, nil
}

type stubLocal struct {
	seed [32]byte
}

func (l *stubLocal) Sign(msg []byte) []byte {
	// Fake signature: 64 zero bytes — proves the interface contract,
	// not cryptographic security.
	out := make([]byte, 64)

	return out
}

func (l *stubLocal) AgreementPublic() []byte {
	out := make([]byte, 32)
	copy(out, l.seed[:])

	return out
}

func (l *stubLocal) Agree(remote []byte) ([]byte, error) {
	if len(remote) != 32 {
		return nil, errors.New("stub: bad remote length")
	}
	out := make([]byte, 32)
	for i := range out {
		out[i] = l.seed[i] ^ remote[i]
	}

	return out, nil
}

func (l *stubLocal) Remote() identity.Remote {
	var pub [32]byte
	copy(pub[:], l.seed[:])

	return stubRemote{pub: pub}
}

type stubRemote struct {
	pub [32]byte
}

func (r stubRemote) Verify(msg, sig []byte) bool { return len(sig) == 64 }

func (r stubRemote) PeerID() identity.PeerID {
	// Derive a 16-byte peer-id by folding the 32-byte pub.
	var h identity.Hash
	for i := range h {
		h[i] = r.pub[i] ^ r.pub[i+16]
	}

	return h
}

func (r stubRemote) AgreementPublic() []byte {
	out := make([]byte, 32)
	copy(out, r.pub[:])

	return out
}

func (r stubRemote) Marshal() []byte {
	out := make([]byte, 1+32)
	out[0] = stubSuiteID
	copy(out[1:], r.pub[:])

	return out
}

// TestStubSuite02_LocalSatisfiesConsumerInterfaces proves that a stub
// Local satisfies every contract Phase 11 consumers depend on, with
// **no modification to consumer code**. This is the key reusability
// claim: external Suite implementations plug in.
func TestStubSuite02_LocalSatisfiesConsumerInterfaces(t *testing.T) {
	t.Parallel()

	local, err := stubSuite{}.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("stub.Generate: %v", err)
	}

	// Compile-time + runtime: stubLocal IS a Signer, KeyAgreement,
	// and Local. If any consumer-required method were missing, the
	// type assertion would fail.
	var _ identity.Signer = local
	var _ identity.KeyAgreement = local
	var _ identity.Local = local

	remote := local.Remote()
	var _ identity.Verifier = remote
	var _ identity.Remote = remote
	var _ identity.PeerID = remote.PeerID()

	// Round-trip the public-half through the suite's own decoder.
	parsed, err := stubSuite{}.ParseRemote(remote.Marshal())
	if err != nil {
		t.Fatalf("stub.ParseRemote: %v", err)
	}
	if parsed.PeerID().Bytes() != remote.PeerID().Bytes() {
		t.Fatalf("stub PeerID drift after round-trip")
	}
}

// TestStubSuite02_DHT_AcceptsForeignPeerID is the canonical proof that
// pkg/dht treats PeerID as a true abstraction. The stub PeerID has no
// relationship with identity.Hash (it's derived from a fake key
// material) — yet it threads through RoutingTable.Closest, Contact,
// Remove, and MemoryStore.Put/Get without error.
func TestStubSuite02_DHT_AcceptsForeignPeerID(t *testing.T) {
	t.Parallel()

	suite := stubSuite{}
	a, _ := suite.Generate(rand.Reader)
	b, _ := suite.Generate(rand.Reader)

	rt := dht.NewRoutingTable(a.Remote().PeerID(), 8)
	rt.Add(dht.Contact{ID: b.Remote().PeerID().Bytes()})

	closest := rt.Closest(b.Remote().PeerID(), 8)
	if len(closest) != 1 {
		t.Fatalf("Closest returned %d contacts via stub PeerID, want 1", len(closest))
	}

	if _, ok := rt.Contact(b.Remote().PeerID()); !ok {
		t.Fatalf("Contact lookup via stub PeerID failed")
	}

	if !rt.Remove(b.Remote().PeerID()) {
		t.Fatalf("Remove via stub PeerID failed")
	}

	store := dht.NewMemoryStore(nil)
	store.Put(b.Remote().PeerID(), []byte("phase 11.10 stub"), time.Hour)
	if _, ok := store.Get(b.Remote().PeerID()); !ok {
		t.Fatalf("MemoryStore.Get via stub PeerID failed")
	}
}

// Note on the registry path: the global LookupSuite registry uses an
// unexported registerSuite function, called from each Suite
// implementation's init() in its production package (suite01.go does
// this for Suite0x01). External Suite implementations follow the same
// pattern. We don't register the stub here because (a) it lives in a
// _test file and shouldn't pollute production registry state, and (b)
// the registry-roundtrip is already proven by TestLookupSuite_Known
// in suite_test.go. The substance of the reusability claim — a
// foreign Suite/Local/Remote slots into pkg/dht and consumer
// interfaces unchanged — is what these tests prove.
