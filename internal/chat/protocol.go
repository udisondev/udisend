// Package chat is the application-level protocol that runs over a webrtc
// DataChannel. Each Message has a kind, a 16-byte ID for ACK matching,
// and a typed body. The wire format is hand-rolled binary (uvarint length
// prefixes); see Encode/Decode.
package chat

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/udisondev/udisend/pkg/wire"
)

// MessageKind discriminates the body payload.
type MessageKind uint8

// Kind values.
const (
	KindText MessageKind = iota + 1
	KindFileOffer
	KindFileChunk
	KindFileEnd
	KindAck
	KindCallInvite
	KindCallAccept
	KindCallReject
	KindCallEnd
	KindTyping
	KindPresence
)

// String returns a human-readable kind for logs/UI.
func (k MessageKind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindFileOffer:
		return "file_offer"
	case KindFileChunk:
		return "file_chunk"
	case KindFileEnd:
		return "file_end"
	case KindAck:
		return "ack"
	case KindCallInvite:
		return "call_invite"
	case KindCallAccept:
		return "call_accept"
	case KindCallReject:
		return "call_reject"
	case KindCallEnd:
		return "call_end"
	case KindTyping:
		return "typing"
	case KindPresence:
		return "presence_ping"
	default:
		return fmt.Sprintf("unknown(%d)", k)
	}
}

// MessageIDSize is the ID length.
const MessageIDSize = 16

// MessageID is a unique-per-conversation identifier used for ACK matching.
type MessageID [MessageIDSize]byte

// NewMessageID returns a fresh random ID.
func NewMessageID() MessageID {
	var m MessageID
	_, _ = rand.Read(m[:])
	return m
}

// MaxBodySize caps message size.
const MaxBodySize = 256 * 1024

const messageVersion byte = 0x01

// Message is the on-wire chat message.
type Message struct {
	ID        MessageID
	Kind      MessageKind
	Timestamp time.Time
	Body      []byte
}

// Errors returned by the package.
var (
	ErrDecode = errors.New("chat: decode failed")
)

// Encode serialises a Message using the project wire format.
func (m *Message) Encode() ([]byte, error) {
	if len(m.Body) > MaxBodySize {
		return nil, fmt.Errorf("%w: body too large", ErrDecode)
	}

	w := wire.NewWriter()
	w.WriteUint8(messageVersion)
	w.WriteUint8(uint8(m.Kind))
	w.WriteFixed(m.ID[:])
	w.WriteUint64(uint64(m.Timestamp.UnixNano()))
	w.WriteBytes(m.Body)

	return w.Bytes(), nil
}

// Decode parses a Message produced by Encode.
func Decode(data []byte) (*Message, error) {
	b := wire.NewBuffer(data)

	ver, err := b.ReadUint8()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}

	if ver != messageVersion {
		return nil, fmt.Errorf("%w: version=%d", ErrDecode, ver)
	}

	kind, err := b.ReadUint8()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}

	idBytes, err := b.ReadFixed(MessageIDSize)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}

	ts, err := b.ReadUint64()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}

	body, err := b.ReadBytes(MaxBodySize)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}

	if err := b.SkipUnknownTLVs(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}

	m := &Message{
		Kind:      MessageKind(kind),
		Timestamp: time.Unix(0, int64(ts)).UTC(),
		Body:      body,
	}

	copy(m.ID[:], idBytes)

	return m, nil
}

// FileOffer is the body of a KindFileOffer message.
type FileOffer struct {
	FileID    MessageID // identifies the transfer; chunks reference it
	Name      string
	Size      uint64
	SHA256Hex string // file content hash (lowercase hex)
}

// EncodeFileOffer serialises a file-offer body.
func EncodeFileOffer(o FileOffer) []byte {
	w := wire.NewWriter()

	w.WriteFixed(o.FileID[:])
	w.WriteString(o.Name)
	w.WriteUint64(o.Size)
	w.WriteString(o.SHA256Hex)

	return w.Bytes()
}

// DecodeFileOffer parses a file-offer body.
func DecodeFileOffer(body []byte) (FileOffer, error) {
	b := wire.NewBuffer(body)

	var o FileOffer

	idBytes, err := b.ReadFixed(MessageIDSize)
	if err != nil {
		return o, err
	}

	copy(o.FileID[:], idBytes)

	name, err := b.ReadString(1024)
	if err != nil {
		return o, err
	}

	o.Name = name

	size, err := b.ReadUint64()
	if err != nil {
		return o, err
	}

	o.Size = size

	hash, err := b.ReadString(128)
	if err != nil {
		return o, err
	}

	o.SHA256Hex = hash

	return o, b.SkipUnknownTLVs()
}

// FileChunk is the body of KindFileChunk.
type FileChunk struct {
	FileID MessageID
	Offset uint64
	Data   []byte
}

// EncodeFileChunk serialises a chunk.
func EncodeFileChunk(c FileChunk) []byte {
	w := wire.NewWriter()
	w.WriteFixed(c.FileID[:])
	w.WriteUint64(c.Offset)
	w.WriteBytes(c.Data)
	return w.Bytes()
}

// DecodeFileChunk parses a chunk body.
func DecodeFileChunk(body []byte) (FileChunk, error) {
	b := wire.NewBuffer(body)

	var c FileChunk

	idBytes, err := b.ReadFixed(MessageIDSize)
	if err != nil {
		return c, err
	}

	copy(c.FileID[:], idBytes)

	off, err := b.ReadUint64()
	if err != nil {
		return c, err
	}

	c.Offset = off

	data, err := b.ReadBytes(MaxBodySize)
	if err != nil {
		return c, err
	}

	c.Data = data

	return c, b.SkipUnknownTLVs()
}

// FileEnd is the body of KindFileEnd: a marker indicating no further chunks.
type FileEnd struct {
	FileID MessageID
}

// EncodeFileEnd / DecodeFileEnd round-trip a FileEnd body.
func EncodeFileEnd(e FileEnd) []byte {
	return e.FileID[:]
}

// DecodeFileEnd parses a FileEnd body.
func DecodeFileEnd(body []byte) (FileEnd, error) {
	if len(body) != MessageIDSize {
		return FileEnd{}, ErrDecode
	}

	var e FileEnd

	copy(e.FileID[:], body)

	return e, nil
}

// Ack body — just the ID being acknowledged.
type Ack struct {
	ID MessageID
}

// EncodeAck / DecodeAck.
func EncodeAck(a Ack) []byte { return a.ID[:] }

// DecodeAck parses an Ack body.
func DecodeAck(body []byte) (Ack, error) {
	if len(body) != MessageIDSize {
		return Ack{}, ErrDecode
	}

	var a Ack
	copy(a.ID[:], body)

	return a, nil
}
