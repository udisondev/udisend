package webrtc_test

import (
	"crypto/rand"
	"errors"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
	uwebrtc "github.com/udisondev/udisend/pkg/webrtc"
)

func TestSignedSDP_Roundtrip(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)
	for _, kind := range []byte{uwebrtc.SDPTypeOffer, uwebrtc.SDPTypeAnswer, uwebrtc.SDPTypeICE, uwebrtc.SDPTypeBye} {
		signed := uwebrtc.SignedSDP{Kind: kind, SDP: "v=0\r\n..."}
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
		if got.SDP != signed.SDP {
			t.Error("sdp")
		}
		if err := got.Verify(id.Public()); err != nil {
			t.Fatalf("verify (kind=%d): %v", kind, err)
		}
	}
}

func TestSignedSDP_RejectsTamper(t *testing.T) {
	t.Parallel()
	id, _ := identity.Generate(rand.Reader)
	signed := uwebrtc.SignedSDP{Kind: uwebrtc.SDPTypeOffer, SDP: "fingerprint:abcd"}
	signed.Sign(id)
	signed.SDP = "fingerprint:DEAD"
	if err := signed.Verify(id.Public()); !errors.Is(err, uwebrtc.ErrSDPSignature) {
		t.Fatalf("err = %v, want ErrSDPSignature", err)
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
