package transport_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/transport"
)

func TestWebRTCAddr_Roundtrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hash identity.Hash
	}{
		{name: "zero hash", hash: identity.Hash{}},
		{
			name: "all-FF hash",
			hash: identity.Hash{
				0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
				0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			},
		},
		{
			name: "ascending hash",
			hash: identity.Hash{
				0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
				0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
			},
		},
		{
			name: "descending hash",
			hash: identity.Hash{
				0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88,
				0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0x00,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			addr := transport.NewWebRTCAddr(tt.hash)
			if addr.Network() != "rtc" {
				t.Errorf("Network() = %q, want %q", addr.Network(), "rtc")
			}
			s := addr.String()
			if !strings.HasPrefix(s, "rtc:") {
				t.Errorf("String() = %q, missing rtc: prefix", s)
			}

			parsed, err := transport.ParseWebRTCAddr(s)
			if err != nil {
				t.Fatalf("ParseWebRTCAddr(%q): %v", s, err)
			}
			if parsed.Peer() != tt.hash {
				t.Errorf("roundtrip mismatch: got %x, want %x", parsed.Peer(), tt.hash)
			}
		})
	}
}

func TestParseWebRTCAddr_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "missing prefix", input: "0102030405060708090a0b0c0d0e0f10"},
		{name: "wrong scheme", input: "udp:0102030405060708090a0b0c0d0e0f10"},
		{name: "garbage hex", input: "rtc:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{name: "too short", input: "rtc:0102"},
		{name: "too long", input: "rtc:0102030405060708090a0b0c0d0e0f1011"},
		{name: "only prefix", input: "rtc:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := transport.ParseWebRTCAddr(tt.input)
			if err == nil {
				t.Errorf("ParseWebRTCAddr(%q) = nil, want error", tt.input)
			}
			if !errors.Is(err, transport.ErrInvalidAddr) {
				t.Errorf("err = %v, want ErrInvalidAddr", err)
			}
		})
	}
}

func TestWebRTCTransport_ImplementsInterface(t *testing.T) {
	// Compile-time assertion in webrtc.go is the real test;
	// this just gives the assertion a runtime touch-point.
	var _ transport.Transport = (*transport.WebRTCTransport)(nil)
}

func TestWebRTCTransport_DialParsesAddress(t *testing.T) {
	t.Parallel()

	// Dial is a pure parse-only API. With Phase 10.5 the transport
	// is functional, so a well-formed address resolves without an
	// underlying connection.
	var w transport.WebRTCTransport

	addr := "rtc:" + strings.Repeat("0", 32)
	got, err := w.Dial(addr)
	if err != nil {
		t.Errorf("Dial(%q): %v", addr, err)
	}
	if got == nil || got.String() != addr {
		t.Errorf("Dial roundtrip = %v, want %q", got, addr)
	}
}

func FuzzWebRTCAddr_RoundTrip(f *testing.F) {
	// Seed corpus: the canonical zero/all-FF/sequential hashes.
	f.Add(make([]byte, identity.HashSize))
	allFF := make([]byte, identity.HashSize)
	for i := range allFF {
		allFF[i] = 0xff
	}
	f.Add(allFF)

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) != identity.HashSize {
			t.Skip()
		}
		var h identity.Hash
		copy(h[:], raw)

		s := transport.NewWebRTCAddr(h).String()
		parsed, err := transport.ParseWebRTCAddr(s)
		if err != nil {
			t.Fatalf("ParseWebRTCAddr(%q) on encoded valid hash: %v", s, err)
		}
		if parsed.Peer() != h {
			t.Fatalf("roundtrip mismatch: got %x, want %x", parsed.Peer(), h)
		}
	})
}
