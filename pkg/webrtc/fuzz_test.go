package webrtc_test

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	uwebrtc "github.com/udisondev/udisend/pkg/webrtc"
)

// FuzzSignedSDPUnmarshal feeds the SDP envelope decoder arbitrary
// bytes. The decoder must never panic, must reject malformed input
// with an error, and must not allocate proportional to attacker-
// controlled length fields without bounds-checking against the
// remaining buffer.
func FuzzSignedSDPUnmarshal(f *testing.F) {
	// Seed with a real signed-and-marshalled envelope so the fuzzer
	// has a realistic starting point (mutations from valid input
	// often surface boundary bugs faster than purely random data).
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	signed := uwebrtc.SignedSDP{
		Kind:      uwebrtc.SDPTypeOffer,
		Recipient: identity.Hash{0xA1, 0xB2},
		IssuedAt:  time.Unix(1700000000, 0).Unix(),
		SDP:       "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n",
	}
	signed.SessionID[0] = 0xDE
	signed.Sign(id)
	if blob, err := signed.MarshalBinary(); err == nil {
		f.Add(blob)
	}
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})

	f.Fuzz(func(t *testing.T, data []byte) {
		var got uwebrtc.SignedSDP
		_ = got.UnmarshalBinary(data)
	})
}
