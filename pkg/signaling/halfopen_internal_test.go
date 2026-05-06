package signaling

import (
	"crypto/rand"
	"net/netip"
	"sync"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
)

// TestAcceptInit_MalformedPayloadReleasesSlot verifies the fix for the
// half-open slot reservation: when ReadMessage fails on an attacker-
// crafted INIT payload, the per-IP counter must return to zero.
//
// Without the deferred release, every malformed-but-otherwise-routable
// INIT from one IP would have permanently consumed a half-open slot
// after we tightened the reservation to happen BEFORE the expensive
// Curve25519 work — exhausting MaxHalfOpenPerIP without any single
// session ever reaching `s.sessions`.
func TestAcceptInit_MalformedPayloadReleasesSlot(t *testing.T) {
	t.Parallel()

	hub := transport.NewMemoryHub()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	tr := hub.NewMemoryTransport()
	t.Cleanup(func() { _ = tr.Close() })

	svc := NewService(Config{
		Identity:  id,
		Transport: tr,
	})
	t.Cleanup(svc.Close)

	from := udpAddrForTest("203.0.113.7:51234")
	ipKey := relayHostKey(from)
	if ipKey == "" {
		t.Fatalf("relayHostKey returned empty for %v", from)
	}

	const burst = MaxHalfOpenPerIP * 4
	var wg sync.WaitGroup
	for range burst {
		wg.Go(func() {
			// Build an envelope routed to us with a corrupt Noise payload.
			// ReadMessage will fail; the reservation must be released by
			// the deferred unwind.
			env := &Envelope{
				Sender:    randomHash(t),
				Recipient: id.Public().DestinationHash(),
				SessionID: randomSessionID(t),
				InnerType: InnerHelloInit,
				Payload:   []byte("not-a-noise-handshake"),
			}
			svc.acceptInit(t.Context(), from, env)
		})
	}
	wg.Wait()

	svc.mu.Lock()
	got := svc.halfOpenByIP[ipKey]
	svc.mu.Unlock()
	if got != 0 {
		t.Fatalf("halfOpenByIP[%q] = %d after malformed burst, want 0", ipKey, got)
	}
}

func udpAddrForTest(s string) *udpAddrTest {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		panic(err)
	}

	return &udpAddrTest{ap: ap}
}

type udpAddrTest struct{ ap netip.AddrPort }

func (a *udpAddrTest) Network() string { return "udp" }
func (a *udpAddrTest) String() string  { return a.ap.String() }

func randomHash(t *testing.T) identity.Hash {
	t.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	return id.Public().DestinationHash()
}

func randomSessionID(t *testing.T) SessionID {
	t.Helper()
	var sid SessionID
	if _, err := rand.Read(sid[:]); err != nil {
		t.Fatal(err)
	}

	return sid
}
