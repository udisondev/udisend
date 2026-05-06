package webrtc_test

import (
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	uwebrtc "github.com/udisondev/udisend/pkg/webrtc"
)

// fixedNow is a stable wall-clock used by tests so signed envelopes verify
// against a deterministic IssuedAt.
var fixedNow = time.Unix(1700000000, 0)

func TestSignedSDP_Roundtrip(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)
	var (
		recipient = identity.Hash{0xA1, 0xB2, 0xC3}
		sid       [uwebrtc.SessionIDSize]byte
	)
	sid[0] = 0xDE
	sid[1] = 0xAD
	for _, kind := range []byte{uwebrtc.SDPTypeOffer, uwebrtc.SDPTypeAnswer, uwebrtc.SDPTypeICE, uwebrtc.SDPTypeBye} {
		signed := uwebrtc.SignedSDP{
			Kind:      kind,
			Recipient: recipient,
			SessionID: sid,
			IssuedAt:  fixedNow.Unix(),
			SDP:       "v=0\r\n...",
		}
		signed.Sign(id)
		blob, err := signed.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		var got uwebrtc.SignedSDP
		if err := got.UnmarshalBinary(blob); err != nil {
			t.Fatal(err)
		}
		if got.Kind != kind {
			t.Errorf("kind = %d, want %d", got.Kind, kind)
		}
		if got.Recipient != recipient {
			t.Errorf("recipient roundtrip mismatch")
		}
		if got.SessionID != sid {
			t.Errorf("sessionID roundtrip mismatch")
		}
		if got.IssuedAt != signed.IssuedAt {
			t.Errorf("issuedAt = %d, want %d", got.IssuedAt, signed.IssuedAt)
		}
		if got.SDP != signed.SDP {
			t.Error("sdp")
		}
		if err := got.Verify(id.Public(), recipient, sid, fixedNow); err != nil {
			t.Fatalf("verify (kind=%d): %v", kind, err)
		}
	}
}

func TestSignedSDP_RejectsTamper(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)
	rcpt := identity.Hash{0x01}
	var sid [uwebrtc.SessionIDSize]byte
	signed := uwebrtc.SignedSDP{
		Kind: uwebrtc.SDPTypeOffer, Recipient: rcpt, SessionID: sid,
		IssuedAt: fixedNow.Unix(), SDP: "fingerprint:abcd",
	}
	signed.Sign(id)
	signed.SDP = "fingerprint:DEAD"
	if err := signed.Verify(id.Public(), rcpt, sid, fixedNow); !errors.Is(err, uwebrtc.ErrSDPSignature) {
		t.Fatalf("err = %v, want ErrSDPSignature", err)
	}
}

// TestSignedSDP_RejectsCrossRecipientReplay covers the binding-field
// defence: a signed offer captured on the wire to peer A must be
// rejected when the same bytes are presented to peer B's verifier.
// Without this, a relay can MITM a different conversation by replaying
// captured offers to the wrong recipient.
func TestSignedSDP_RejectsCrossRecipientReplay(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)

	intended := identity.Hash{0xAA}
	other := identity.Hash{0xBB}
	var sid [uwebrtc.SessionIDSize]byte

	signed := uwebrtc.SignedSDP{
		Kind: uwebrtc.SDPTypeOffer, Recipient: intended, SessionID: sid,
		IssuedAt: fixedNow.Unix(), SDP: "v=0\r\n",
	}
	signed.Sign(id)

	if err := signed.Verify(id.Public(), other, sid, fixedNow); !errors.Is(err, uwebrtc.ErrSDPRecipient) {
		t.Errorf("cross-recipient replay accepted; err=%v want ErrSDPRecipient", err)
	}
}

// TestSignedSDP_RejectsCrossSessionReplay: an offer captured during
// session X must not verify in session Y, even between the same pair of
// peers. This is what makes the relay unable to splice a fresh handshake
// onto a stale captured offer.
func TestSignedSDP_RejectsCrossSessionReplay(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)

	rcpt := identity.Hash{0xAA}
	var sidIntended, sidOther [uwebrtc.SessionIDSize]byte
	sidIntended[0] = 1
	sidOther[0] = 2

	signed := uwebrtc.SignedSDP{
		Kind: uwebrtc.SDPTypeOffer, Recipient: rcpt, SessionID: sidIntended,
		IssuedAt: fixedNow.Unix(), SDP: "v=0\r\n",
	}
	signed.Sign(id)

	if err := signed.Verify(id.Public(), rcpt, sidOther, fixedNow); !errors.Is(err, uwebrtc.ErrSDPSession) {
		t.Errorf("cross-session replay accepted; err=%v want ErrSDPSession", err)
	}
}

// TestSignedSDP_RejectsStaleReplay: an offer captured a day ago must
// not verify today.
func TestSignedSDP_RejectsStaleReplay(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)

	rcpt := identity.Hash{0xAA}
	var sid [uwebrtc.SessionIDSize]byte

	yesterday := fixedNow.Add(-24 * time.Hour)
	signed := uwebrtc.SignedSDP{
		Kind: uwebrtc.SDPTypeOffer, Recipient: rcpt, SessionID: sid,
		IssuedAt: yesterday.Unix(), SDP: "v=0\r\n",
	}
	signed.Sign(id)

	if err := signed.Verify(id.Public(), rcpt, sid, fixedNow); !errors.Is(err, uwebrtc.ErrSDPClockSkew) {
		t.Errorf("stale replay accepted; err=%v want ErrSDPClockSkew", err)
	}
}

// TestSignedSDP_AcceptsWithinSkew confirms that legitimate clock drift
// between peers is tolerated.
func TestSignedSDP_AcceptsWithinSkew(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)

	rcpt := identity.Hash{0xAA}
	var sid [uwebrtc.SessionIDSize]byte

	for _, off := range []time.Duration{-2 * time.Minute, 0, 2 * time.Minute} {
		signed := uwebrtc.SignedSDP{
			Kind: uwebrtc.SDPTypeOffer, Recipient: rcpt, SessionID: sid,
			IssuedAt: fixedNow.Add(off).Unix(), SDP: "v=0\r\n",
		}
		signed.Sign(id)
		if err := signed.Verify(id.Public(), rcpt, sid, fixedNow); err != nil {
			t.Errorf("offset %s rejected: %v", off, err)
		}
	}
}

func TestKindString_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, k := range []byte{uwebrtc.SDPTypeOffer, uwebrtc.SDPTypeAnswer, uwebrtc.SDPTypeICE, uwebrtc.SDPTypeBye} {
		s := uwebrtc.KindString(k)
		if s == "unknown" {
			t.Errorf("kind %d → unknown", k)
		}
		got, ok := uwebrtc.KindFromString(s)
		if !ok || got != k {
			t.Errorf("KindFromString(%q) = %d, %v; want %d, true", s, got, ok, k)
		}
	}
}
