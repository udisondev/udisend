package identity_test

import (
	"crypto/rand"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
)

func BenchmarkSign(b *testing.B) {
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	msg := make([]byte, 256)
	_, _ = rand.Read(msg)
	b.ReportAllocs()
	for b.Loop() {
		_ = id.Sign(msg)
	}
}

func BenchmarkVerify(b *testing.B) {
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	msg := make([]byte, 256)
	_, _ = rand.Read(msg)
	sig := id.Sign(msg)
	pub := id.Public()
	b.ReportAllocs()
	for b.Loop() {
		_ = pub.Verify(msg, sig)
	}
}

func BenchmarkDestinationHash(b *testing.B) {
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	pub := id.Public()
	b.ReportAllocs()
	for b.Loop() {
		_ = pub.DestinationHash()
	}
}

func BenchmarkSharedSecret(b *testing.B) {
	a, _ := identity.Generate(rand.Reader)
	c, _ := identity.Generate(rand.Reader)
	pub := c.Public()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = a.SharedSecret(pub)
	}
}
