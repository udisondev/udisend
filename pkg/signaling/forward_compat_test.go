package signaling_test

import (
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/wire"
)

// TestEnvelope_TolerantOfTrailingTLVs is the forward-compatibility
// regression test for design.md §7: a future client may append unknown
// TLV extensions to the end of an envelope payload; current decoders
// must accept the message and ignore them.
func TestEnvelope_TolerantOfTrailingTLVs(t *testing.T) {
	t.Parallel()
	sid, err := signaling.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	env := &signaling.Envelope{
		Recipient: identity.Hash{1, 2, 3},
		Sender:    identity.Hash{4, 5, 6},
		SessionID: sid,
		InnerType: signaling.InnerData,
		Payload:   []byte("hello"),
	}
	blob, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}

	// Splice an unknown TLV between the envelope body and the outer frame.
	// We rebuild the wire frame manually so the trailer is part of the
	// envelope payload (not a separate frame).
	typ, body, err := wire.DecodeFrame(blob)
	if err != nil {
		t.Fatal(err)
	}
	w := wire.NewWriter()
	w.WriteFixed(body)
	w.WriteTLV(0x77, []byte("future-extension-payload"))
	patched, err := wire.EncodeFrame(typ, w.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	got, err := signaling.Decode(patched)
	if err != nil {
		t.Fatalf("decode with unknown trailer: %v", err)
	}
	if got.InnerType != env.InnerType {
		t.Fatalf("InnerType lost: got %d", got.InnerType)
	}
	if string(got.Payload) != "hello" {
		t.Fatalf("Payload corrupted: %q", got.Payload)
	}
}
