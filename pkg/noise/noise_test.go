package noise_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/noise"
)

func TestXK_FullHandshakeAndDataExchange(t *testing.T) {
	t.Parallel()
	alice, _ := identity.Generate(rand.Reader)
	bob, _ := identity.Generate(rand.Reader)

	initiator, err := noise.NewInitiator(alice, bob.Public())
	if err != nil {
		t.Fatal(err)
	}
	responder, err := noise.NewResponder(bob)
	if err != nil {
		t.Fatal(err)
	}

	// Message 1: A → B
	m1, err := initiator.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.ReadMessage(m1); err != nil {
		t.Fatal(err)
	}

	// Message 2: B → A
	m2, err := responder.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initiator.ReadMessage(m2); err != nil {
		t.Fatal(err)
	}

	// Message 3: A → B
	m3, err := initiator.WriteMessage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.ReadMessage(m3); err != nil {
		t.Fatal(err)
	}

	if !initiator.Done() || !responder.Done() {
		t.Fatal("handshake not done after 3 messages")
	}

	peerStatic, _ := responder.PeerStatic()
	alicePub := alice.Public().XPub
	if !bytes.Equal(peerStatic, alicePub[:]) {
		t.Fatal("responder didn't authenticate initiator's static")
	}

	// Bidirectional encrypt / decrypt.
	plain := []byte("hello, signaling")
	ad := []byte("session-1")
	ct, err := initiator.Encrypt(plain, ad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := responder.Decrypt(ct, ad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q", got)
	}
	// Reverse direction.
	ct2, _ := responder.Encrypt([]byte("ack"), nil)
	got2, err := initiator.Decrypt(ct2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != "ack" {
		t.Fatal("reverse decrypt")
	}
}

func TestXK_TamperedHandshakeFails(t *testing.T) {
	t.Parallel()
	alice, _ := identity.Generate(rand.Reader)
	bob, _ := identity.Generate(rand.Reader)
	initiator, _ := noise.NewInitiator(alice, bob.Public())
	responder, _ := noise.NewResponder(bob)

	m1, _ := initiator.WriteMessage(nil)
	m1[0] ^= 0xFF
	if _, err := responder.ReadMessage(m1); err == nil {
		t.Fatal("tampered message must fail")
	}
}

func TestXK_WrongResponderStatic(t *testing.T) {
	t.Parallel()
	alice, _ := identity.Generate(rand.Reader)
	mallory, _ := identity.Generate(rand.Reader)
	bob, _ := identity.Generate(rand.Reader)

	// initiator targets bob, but mallory tries to respond.
	initiator, _ := noise.NewInitiator(alice, bob.Public())
	mResponder, _ := noise.NewResponder(mallory)

	m1, _ := initiator.WriteMessage(nil)
	if _, err := mResponder.ReadMessage(m1); err != nil {
		// flynn/noise may reject m1 outright since pattern is XK and
		// mallory's static doesn't match the prologue. Either outcome
		// (here or in m2 read) is acceptable; just assert at least one
		// step fails.
		return
	}
	m2, _ := mResponder.WriteMessage(nil)
	if _, err := initiator.ReadMessage(m2); err == nil {
		t.Fatal("initiator should reject responder using a different static key")
	}
}

// TestEncrypt_RefusesPastMessageBudget is the nonce-exhaustion guard:
// a session that has issued MaxMessagesPerKey AEAD operations MUST
// refuse further use rather than approach the AEAD nonce-collision
// boundary. Real signaling never reaches this — the test pre-loads the
// counter via the public Encrypt path with low budget injection through
// the test-only API.
func TestEncrypt_RefusesPastMessageBudget(t *testing.T) {
	t.Parallel()
	alice, _ := identity.Generate(rand.Reader)
	bob, _ := identity.Generate(rand.Reader)
	initiator, _ := noise.NewInitiator(alice, bob.Public())
	responder, _ := noise.NewResponder(bob)

	// Drive the handshake so cipher states exist.
	m1, _ := initiator.WriteMessage(nil)
	_, _ = responder.ReadMessage(m1)
	m2, _ := responder.WriteMessage(nil)
	_, _ = initiator.ReadMessage(m2)
	m3, _ := initiator.WriteMessage(nil)
	_, _ = responder.ReadMessage(m3)

	// Saturate the budget directly — we exposed it as a constant, so
	// the test is bound to the public API surface.
	initiator.SetSendCountForTest(noise.MaxMessagesPerKey)

	if _, err := initiator.Encrypt([]byte("over"), nil); !errors.Is(err, noise.ErrSessionExhausted) {
		t.Fatalf("Encrypt at budget = %v, want ErrSessionExhausted", err)
	}
}

func TestEncrypt_BeforeHandshake(t *testing.T) {
	t.Parallel()
	alice, _ := identity.Generate(rand.Reader)
	bob, _ := identity.Generate(rand.Reader)
	initiator, _ := noise.NewInitiator(alice, bob.Public())
	if _, err := initiator.Encrypt([]byte("x"), nil); !errors.Is(err, noise.ErrSessionNotReady) {
		t.Fatalf("err = %v, want ErrSessionNotReady", err)
	}
}
