// Package wire provides the project-wide binary codec primitives used by
// the DHT, presence and signaling layers. The format is hand-rolled and
// length-prefixed via uvarint to keep parsing trivial and fuzz-friendly:
//
//	bytes(short)     := uvarint(length) || raw[length]
//	bytes(fixed N)   := raw[N]
//	uint16/uint32/.. := big-endian
//
// Frames are wrapped by an outer envelope:
//
//	frame := version(1) || type(1) || uvarint(payloadLen) || payload[payloadLen]
//
// Versions are bumped on incompatible payload changes; `type` discriminates
// payload variants. The codec is intentionally minimal — anything richer
// (CBOR / protobuf) would replace this whole package and is reserved for
// post-MVP cross-language interop.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// CurrentVersion is the wire-format version emitted by Encoders. Decoders
// reject frames with a different version.
const CurrentVersion byte = 0x01

// MaxFrameSize caps the size of any single decoded frame. Production traffic
// should fit comfortably below this; the limit exists to stop a malicious
// peer from forcing an OOM during decode.
const MaxFrameSize = 1 << 20 // 1 MiB

// Errors returned by the package.
var (
	ErrShortBuffer    = errors.New("wire: buffer too short")
	ErrFrameTooLarge  = errors.New("wire: frame exceeds MaxFrameSize")
	ErrUnknownVersion = errors.New("wire: unknown version")
	ErrTrailingBytes  = errors.New("wire: trailing bytes after payload")
)

// EncodeFrame returns version || type || uvarint(len(payload)) || payload.
func EncodeFrame(typ byte, payload []byte) ([]byte, error) {
	if len(payload) > MaxFrameSize {
		return nil, ErrFrameTooLarge
	}
	out := make([]byte, 2+binary.MaxVarintLen32+len(payload))
	out[0] = CurrentVersion
	out[1] = typ
	n := binary.PutUvarint(out[2:], uint64(len(payload)))
	copy(out[2+n:], payload)
	return out[:2+n+len(payload)], nil
}

// frameWriterPrefix is the maximum bytes a frame header can occupy
// (version + type + uvarint32 length).
const frameWriterPrefix = 2 + binary.MaxVarintLen32

// NewFrameWriter returns a Buffer ready for in-place frame construction:
// the caller appends the body via the standard WriteX methods, then
// FinishFrame patches the version / type / length prefix into reserved
// front bytes and returns the contiguous frame slice. Avoids the double
// allocation of NewWriter + EncodeFrame for hot encode paths.
//
// bodyHint is the expected body size; the underlying buffer is sized so a
// typical frame fits without reallocation. The frame writer is write-only;
// reading from it before FinishFrame is undefined.
func NewFrameWriter(bodyHint int) *Buffer {
	c := frameWriterPrefix + bodyHint
	if c < frameWriterPrefix+64 {
		c = frameWriterPrefix + 64
	}
	buf := make([]byte, frameWriterPrefix, c)
	return &Buffer{buf: buf}
}

// FinishFrame finalises a frame started with NewFrameWriter by writing the
// version, type, and uvarint(body-length) into the reserved prefix region
// and returning the contiguous frame slice. The Buffer is consumed.
func FinishFrame(b *Buffer, typ byte) ([]byte, error) {
	bodyLen := len(b.buf) - frameWriterPrefix
	if bodyLen < 0 {
		return nil, fmt.Errorf("wire: FinishFrame called on non-frame Buffer")
	}
	if bodyLen > MaxFrameSize {
		return nil, ErrFrameTooLarge
	}
	// Compute varint encoding length so we know where the real prefix starts.
	var tmp [binary.MaxVarintLen32]byte
	n := binary.PutUvarint(tmp[:], uint64(bodyLen))
	start := frameWriterPrefix - 2 - n
	b.buf[start] = CurrentVersion
	b.buf[start+1] = typ
	copy(b.buf[start+2:], tmp[:n])
	return b.buf[start:], nil
}

// DecodeFrame parses a frame produced by EncodeFrame. It returns the type
// byte, payload (a sub-slice of `data`, not copied), and an error if the
// frame is malformed.
func DecodeFrame(data []byte) (typ byte, payload []byte, err error) {
	if len(data) < 3 {
		return 0, nil, ErrShortBuffer
	}
	if data[0] != CurrentVersion {
		return 0, nil, fmt.Errorf("%w: got %d", ErrUnknownVersion, data[0])
	}
	typ = data[1]
	length, n := binary.Uvarint(data[2:])
	if n <= 0 {
		return 0, nil, ErrShortBuffer
	}
	if length > MaxFrameSize {
		return 0, nil, ErrFrameTooLarge
	}
	rest := data[2+n:]
	if uint64(len(rest)) < length {
		return 0, nil, ErrShortBuffer
	}
	if uint64(len(rest)) > length {
		return 0, nil, ErrTrailingBytes
	}
	return typ, rest[:length], nil
}

// Buffer is a tiny append-only / read-cursor combo for hand-rolling
// codecs. It is not safe for concurrent use.
type Buffer struct {
	buf []byte
	off int
}

// NewBuffer wraps existing bytes for reading.
func NewBuffer(b []byte) *Buffer { return &Buffer{buf: b} }

// NewWriter returns a Buffer ready for appending.
func NewWriter() *Buffer { return &Buffer{buf: make([]byte, 0, 64)} }

// Bytes returns the underlying byte slice (shared, not copied).
func (b *Buffer) Bytes() []byte { return b.buf }

// Remaining reports how many bytes have not yet been read.
func (b *Buffer) Remaining() int { return len(b.buf) - b.off }

// WriteUint8 / WriteUint16 / WriteUint32 / WriteUint64 append big-endian.
func (b *Buffer) WriteUint8(v byte)    { b.buf = append(b.buf, v) }
func (b *Buffer) WriteUint16(v uint16) { b.buf = binary.BigEndian.AppendUint16(b.buf, v) }
func (b *Buffer) WriteUint32(v uint32) { b.buf = binary.BigEndian.AppendUint32(b.buf, v) }
func (b *Buffer) WriteUint64(v uint64) { b.buf = binary.BigEndian.AppendUint64(b.buf, v) }

// WriteUvarint appends a uvarint-encoded integer.
func (b *Buffer) WriteUvarint(v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	b.buf = append(b.buf, tmp[:n]...)
}

// WriteBytes appends a length-prefixed (uvarint) byte slice.
func (b *Buffer) WriteBytes(v []byte) {
	b.WriteUvarint(uint64(len(v)))
	b.buf = append(b.buf, v...)
}

// WriteFixed appends raw bytes (no length prefix).
func (b *Buffer) WriteFixed(v []byte) { b.buf = append(b.buf, v...) }

// WriteString is a convenience around WriteBytes.
func (b *Buffer) WriteString(s string) {
	b.WriteUvarint(uint64(len(s)))
	b.buf = append(b.buf, s...)
}

// ReadUint8 / ReadUint16 / ReadUint32 / ReadUint64 — big-endian readers.
func (b *Buffer) ReadUint8() (byte, error) {
	if b.Remaining() < 1 {
		return 0, ErrShortBuffer
	}
	v := b.buf[b.off]
	b.off++
	return v, nil
}

func (b *Buffer) ReadUint16() (uint16, error) {
	if b.Remaining() < 2 {
		return 0, ErrShortBuffer
	}
	v := binary.BigEndian.Uint16(b.buf[b.off:])
	b.off += 2
	return v, nil
}

func (b *Buffer) ReadUint32() (uint32, error) {
	if b.Remaining() < 4 {
		return 0, ErrShortBuffer
	}
	v := binary.BigEndian.Uint32(b.buf[b.off:])
	b.off += 4
	return v, nil
}

func (b *Buffer) ReadUint64() (uint64, error) {
	if b.Remaining() < 8 {
		return 0, ErrShortBuffer
	}
	v := binary.BigEndian.Uint64(b.buf[b.off:])
	b.off += 8
	return v, nil
}

// ReadUvarint decodes the next uvarint.
func (b *Buffer) ReadUvarint() (uint64, error) {
	v, n := binary.Uvarint(b.buf[b.off:])
	if n <= 0 {
		return 0, ErrShortBuffer
	}
	b.off += n
	return v, nil
}

// ReadBytes reads a uvarint-prefixed byte slice. The returned slice is a
// copy so callers can hold onto it past the buffer's lifetime.
func (b *Buffer) ReadBytes(maxLen int) ([]byte, error) {
	length, err := b.ReadUvarint()
	if err != nil {
		return nil, err
	}
	if length > uint64(maxLen) {
		return nil, fmt.Errorf("wire: bytes len %d exceeds max %d", length, maxLen)
	}
	if uint64(b.Remaining()) < length {
		return nil, ErrShortBuffer
	}
	out := make([]byte, length)
	copy(out, b.buf[b.off:b.off+int(length)])
	b.off += int(length)
	return out, nil
}

// ReadFixed reads exactly n raw bytes.
func (b *Buffer) ReadFixed(n int) ([]byte, error) {
	if b.Remaining() < n {
		return nil, ErrShortBuffer
	}
	out := make([]byte, n)
	copy(out, b.buf[b.off:b.off+n])
	b.off += n
	return out, nil
}

// ReadFixedInto reads exactly len(dst) raw bytes into dst, avoiding the
// intermediate allocation of ReadFixed. Useful for fixed-size fields that
// land in pre-existing arrays (TxID, NodeID, Hash).
func (b *Buffer) ReadFixedInto(dst []byte) error {
	n := len(dst)
	if b.Remaining() < n {
		return ErrShortBuffer
	}
	copy(dst, b.buf[b.off:b.off+n])
	b.off += n
	return nil
}

// ReadString reads a uvarint-prefixed UTF-8 string in a single allocation
// — the previous implementation went through ReadBytes which paid for the
// byte slice and then again for the string conversion.
func (b *Buffer) ReadString(maxLen int) (string, error) {
	length, err := b.ReadUvarint()
	if err != nil {
		return "", err
	}
	if length > uint64(maxLen) {
		return "", fmt.Errorf("wire: string len %d exceeds max %d", length, maxLen)
	}
	if uint64(b.Remaining()) < length {
		return "", ErrShortBuffer
	}
	// string(byteSlice) copies once into the heap-allocated string body.
	s := string(b.buf[b.off : b.off+int(length)])
	b.off += int(length)
	return s, nil
}

// AssertEmpty returns ErrTrailingBytes if there are unread bytes left.
func (b *Buffer) AssertEmpty() error {
	if b.Remaining() != 0 {
		return ErrTrailingBytes
	}
	return nil
}

// MaxExtensions caps the number of unknown TLV extensions a single decode
// will tolerate; past this we assume the input is malicious or malformed.
const MaxExtensions = 32

// SkipUnknownTLVs consumes any bytes remaining in the buffer as a sequence
// of `tag(uvarint) || length(uvarint) || value[length]` extension blocks
// and discards them. This is the forward-compatibility hook described in
// p2p-messenger-design.md §7: "extensions (TLV) — опциональные поля в
// конце; неизвестные пропускаются". Returns ErrShortBuffer / ErrTrailingBytes
// only if the trailing bytes do not form valid TLVs at all.
//
// Callers that consume known leading fields and then want to be tolerant
// of forward-compatible additions should call SkipUnknownTLVs instead of
// AssertEmpty before returning success.
func (b *Buffer) SkipUnknownTLVs() error {
	for i := 0; b.Remaining() > 0; i++ {
		if i >= MaxExtensions {
			return fmt.Errorf("wire: more than %d trailing extensions", MaxExtensions)
		}
		if _, err := b.ReadUvarint(); err != nil {
			return err
		}
		length, err := b.ReadUvarint()
		if err != nil {
			return err
		}
		if length > MaxFrameSize {
			return ErrFrameTooLarge
		}
		if uint64(b.Remaining()) < length {
			return ErrShortBuffer
		}
		b.off += int(length)
	}
	return nil
}

// WriteTLV appends a `tag || length || value` extension block. Receivers
// that know `tag` parse the value; receivers that don't recognise it skip
// the block via SkipUnknownTLVs and continue.
func (b *Buffer) WriteTLV(tag uint64, value []byte) {
	b.WriteUvarint(tag)
	b.WriteUvarint(uint64(len(value)))
	b.WriteFixed(value)
}

// Discard skips ahead by n bytes. Returns ErrShortBuffer if not enough
// remain.
func (b *Buffer) Discard(n int) error {
	if b.Remaining() < n {
		return ErrShortBuffer
	}
	b.off += n
	return nil
}

// Compile-time checks that we can satisfy a couple of common interfaces if
// callers want them.
var _ io.Reader = (*Buffer)(nil)

// Read implements io.Reader so Buffer can be passed where standard library
// helpers expect one.
func (b *Buffer) Read(p []byte) (int, error) {
	if b.Remaining() == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.buf[b.off:])
	b.off += n
	return n, nil
}
