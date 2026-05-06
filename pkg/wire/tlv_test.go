package wire_test

import (
	"errors"
	"testing"

	"github.com/udisondev/udisend/pkg/wire"
)

func TestSkipUnknownTLVs_EmptyBuffer(t *testing.T) {
	t.Parallel()
	b := wire.NewBuffer(nil)
	if err := b.SkipUnknownTLVs(); err != nil {
		t.Fatalf("empty buffer should be ok, got %v", err)
	}
}

func TestSkipUnknownTLVs_ConsumesValidTrailing(t *testing.T) {
	t.Parallel()
	w := wire.NewWriter()
	w.WriteUint8(0xAA) // pretend known leading field
	w.WriteTLV(0x10, []byte("future-feature"))
	w.WriteTLV(0x20, []byte{1, 2, 3, 4})

	r := wire.NewBuffer(w.Bytes())
	if got, _ := r.ReadUint8(); got != 0xAA {
		t.Fatalf("leading byte: got %#x", got)
	}
	if err := r.SkipUnknownTLVs(); err != nil {
		t.Fatalf("SkipUnknownTLVs: %v", err)
	}
	if r.Remaining() != 0 {
		t.Fatalf("expected buffer drained, %d remain", r.Remaining())
	}
}

func TestSkipUnknownTLVs_RejectsTruncated(t *testing.T) {
	t.Parallel()
	w := wire.NewWriter()
	w.WriteUvarint(0x10)          // tag
	w.WriteUvarint(8)             // length=8
	w.WriteFixed([]byte("short")) // only 5 bytes — buffer is now truncated
	r := wire.NewBuffer(w.Bytes())
	if err := r.SkipUnknownTLVs(); !errors.Is(err, wire.ErrShortBuffer) {
		t.Fatalf("expected ErrShortBuffer, got %v", err)
	}
}

func TestSkipUnknownTLVs_BoundsRunaway(t *testing.T) {
	t.Parallel()
	w := wire.NewWriter()
	for range wire.MaxExtensions + 1 {
		w.WriteTLV(0xFF, nil)
	}
	r := wire.NewBuffer(w.Bytes())
	if err := r.SkipUnknownTLVs(); err == nil {
		t.Fatal("expected runaway-extension error")
	}
}
