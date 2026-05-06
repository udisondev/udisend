package identity

import (
	"io"
)

// Suite0x01 is the reference crypto-suite registered for algorithm-id
// byte 0x01: Ed25519 signatures, X25519 key-agreement,
// ChaCha20-Poly1305 AEAD, BLAKE2b hashing.
//
// Suite0x01.Generate returns *Identity directly; Suite0x01.ParseRemote
// returns PublicIdentity. Both concrete types satisfy the Local /
// Remote interfaces (compile-time assertions in identity.go), so no
// adapter wrappers are needed — internal udisend code keeps using the
// concrete types, external Suite-aware consumers see the abstract
// interfaces.
var Suite0x01 Suite = suite01{}

func init() {
	registerSuite(Suite0x01)
}

type suite01 struct{}

func (suite01) ID() byte { return PublicMarshalVersion }

func (suite01) Generate(r io.Reader) (Local, error) {
	id, err := Generate(r)
	if err != nil {
		return nil, err
	}

	return id, nil
}

func (suite01) ParseRemote(blob []byte) (Remote, error) {
	var p PublicIdentity
	if err := p.UnmarshalBinary(blob); err != nil {
		return nil, err
	}

	return p, nil
}
