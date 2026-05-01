package wire_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/udisondev/udisend/pkg/wire"
)

func TestFrame_Roundtrip(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		nil,
		[]byte{},
		[]byte("hello"),
		bytes.Repeat([]byte{0xAB}, 1024),
	}
	for _, payload := range cases {
		blob, err := wire.EncodeFrame(0x42, payload)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		typ, got, err := wire.DecodeFrame(blob)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if typ != 0x42 {
			t.Errorf("type = %d", typ)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("payload mismatch")
		}
	}
}

func TestFrame_RejectsBadVersion(t *testing.T) {
	t.Parallel()
	blob, _ := wire.EncodeFrame(0x01, []byte("x"))
	blob[0] = 0xEE
	_, _, err := wire.DecodeFrame(blob)
	if !errors.Is(err, wire.ErrUnknownVersion) {
		t.Fatalf("err = %v, want ErrUnknownVersion", err)
	}
}

func TestFrame_RejectsTrailingBytes(t *testing.T) {
	t.Parallel()
	blob, _ := wire.EncodeFrame(0x01, []byte("x"))
	blob = append(blob, 0xFF)
	_, _, err := wire.DecodeFrame(blob)
	if !errors.Is(err, wire.ErrTrailingBytes) {
		t.Fatalf("err = %v, want ErrTrailingBytes", err)
	}
}

func TestFrame_RejectsTruncated(t *testing.T) {
	t.Parallel()
	blob, _ := wire.EncodeFrame(0x01, bytes.Repeat([]byte{1}, 100))
	for i := range blob {
		_, _, err := wire.DecodeFrame(blob[:i])
		if err == nil {
			t.Errorf("truncated len=%d unexpectedly accepted", i)
		}
	}
}

func TestBuffer_Roundtrip(t *testing.T) {
	t.Parallel()
	w := wire.NewWriter()
	w.WriteUint8(0xAB)
	w.WriteUint16(0xCAFE)
	w.WriteUint32(0xDEADBEEF)
	w.WriteUint64(0x0102030405060708)
	w.WriteUvarint(1234567890)
	w.WriteBytes([]byte("hi"))
	w.WriteFixed([]byte{1, 2, 3})
	w.WriteString("world")

	r := wire.NewBuffer(w.Bytes())
	if v, _ := r.ReadUint8(); v != 0xAB {
		t.Fatal("u8")
	}
	if v, _ := r.ReadUint16(); v != 0xCAFE {
		t.Fatal("u16")
	}
	if v, _ := r.ReadUint32(); v != 0xDEADBEEF {
		t.Fatal("u32")
	}
	if v, _ := r.ReadUint64(); v != 0x0102030405060708 {
		t.Fatal("u64")
	}
	if v, _ := r.ReadUvarint(); v != 1234567890 {
		t.Fatal("uvarint")
	}
	if v, _ := r.ReadBytes(64); string(v) != "hi" {
		t.Fatal("bytes")
	}
	if v, _ := r.ReadFixed(3); !bytes.Equal(v, []byte{1, 2, 3}) {
		t.Fatal("fixed")
	}
	if v, _ := r.ReadString(64); v != "world" {
		t.Fatal("string")
	}
	if err := r.AssertEmpty(); err != nil {
		t.Fatalf("trailing bytes: %v", err)
	}
}

func TestBuffer_RejectsExcessiveLength(t *testing.T) {
	t.Parallel()
	w := wire.NewWriter()
	w.WriteUvarint(1 << 31)
	r := wire.NewBuffer(w.Bytes())
	if _, err := r.ReadBytes(64); err == nil {
		t.Fatalf("ReadBytes must reject excessive length")
	}
}

func TestBuffer_ShortBuffer(t *testing.T) {
	t.Parallel()
	r := wire.NewBuffer(nil)
	if _, err := r.ReadUint8(); !errors.Is(err, wire.ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
	if _, err := r.ReadUint16(); !errors.Is(err, wire.ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
	if _, err := r.ReadFixed(1); !errors.Is(err, wire.ErrShortBuffer) {
		t.Fatalf("err = %v, want ErrShortBuffer", err)
	}
}

func FuzzDecodeFrame(f *testing.F) {
	f.Add([]byte{0x01, 0x42, 0x00})
	f.Add([]byte{})
	f.Add([]byte{0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _ = wire.DecodeFrame(data)
	})
}
