package identity_test

import (
	"crypto/rand"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
)

func FuzzIdentityUnmarshal(f *testing.F) {
	id := mustGenerate(&testing.T{})
	if blob, err := id.MarshalBinary(); err == nil {
		f.Add(blob)
	}
	f.Add([]byte{})
	f.Add([]byte{0x01})
	f.Add([]byte{0xFF, 0x00, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		var got identity.Identity
		_ = got.UnmarshalBinary(data) // must never panic
	})
}

func FuzzPublicIdentityUnmarshal(f *testing.F) {
	id := mustGenerate(&testing.T{})
	if blob, err := id.Public().MarshalBinary(); err == nil {
		f.Add(blob)
	}
	f.Add([]byte{})
	f.Add([]byte{0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		var got identity.PublicIdentity
		_ = got.UnmarshalBinary(data)
	})
}

func FuzzVerify(f *testing.F) {
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	pub := id.Public()
	sig := id.Sign([]byte("hello"))
	f.Add([]byte("hello"), sig[:])
	f.Add([]byte{}, []byte{})
	f.Fuzz(func(t *testing.T, msg, rawSig []byte) {
		var s identity.Signature
		copy(s[:], rawSig)
		_ = pub.Verify(msg, s) // must never panic
	})
}
