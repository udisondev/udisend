package signaling_test

import (
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/signaling"
)

// TestEnvelope_DecodesEmbedderKinds verifies that any inner-type byte
// in the embedder range (>= 0x06) round-trips through Encode/Decode
// untouched. The wire format is unchanged; pkg/signaling does not
// know which embedder owns which kind.
func TestEnvelope_DecodesEmbedderKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		innerType byte
	}{
		{name: "embedder 0x06", innerType: 0x06},
		{name: "embedder 0x07", innerType: 0x07},
		{name: "embedder 0x08", innerType: 0x08},
		{name: "embedder 0x42", innerType: 0x42},
		{name: "embedder 0xff", innerType: 0xff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := &signaling.Envelope{
				Recipient: identity.Hash{0x01, 0x02, 0x03},
				Sender:    identity.Hash{0x10, 0x20, 0x30},
				SessionID: signaling.SessionID{0xa, 0xb, 0xc},
				InnerType: tt.innerType,
				Payload:   []byte("dummy-payload"),
			}

			frame, err := env.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := signaling.Decode(frame)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if got.InnerType != tt.innerType {
				t.Errorf("InnerType = %d, want %d", got.InnerType, tt.innerType)
			}
			if string(got.Payload) != string(env.Payload) {
				t.Errorf("Payload = %q, want %q", got.Payload, env.Payload)
			}
		})
	}
}
