package dht

import (
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/udisondev/udisend/pkg/wire"
)

// Message type codes used inside wire frames.
const (
	MsgPing       byte = 0x01
	MsgPong       byte = 0x02
	MsgFindNode   byte = 0x03
	MsgNodes      byte = 0x04
	MsgStore      byte = 0x05
	MsgStoreOK    byte = 0x06
	MsgFindValue  byte = 0x07
	MsgValue      byte = 0x08
	// MsgRelay is used by pkg/signaling for opaque payload routing through
	// DHT nodes. We define the constant here so the demux table is in one
	// place; the payload format is defined in pkg/signaling.
	MsgRelay byte = 0x10
)

// TxIDSize is the length of a transaction identifier.
const TxIDSize = 16

// TxID is a random transaction identifier matching requests to responses.
type TxID [TxIDSize]byte

// NewTxID returns a fresh random TxID.
func NewTxID() TxID {
	var t TxID
	_, _ = rand.Read(t[:])
	return t
}

// Header is the common prefix of every DHT message.
type Header struct {
	TxID    TxID
	SrcID   NodeID
	SrcAddr string
}

// MaxAddrLen caps the length of a serialised transport address.
const MaxAddrLen = 256

// MaxStoreValue caps stored value size. Presence records (the only thing
// stored today) are well under this.
const MaxStoreValue = 4 * 1024

func (h *Header) write(b *wire.Buffer) {
	b.WriteFixed(h.TxID[:])
	b.WriteFixed(h.SrcID[:])
	b.WriteString(h.SrcAddr)
}

func (h *Header) read(b *wire.Buffer) error {
	tx, err := b.ReadFixed(TxIDSize)
	if err != nil {
		return err
	}
	copy(h.TxID[:], tx)
	srcID, err := b.ReadFixed(int(IDBits / 8))
	if err != nil {
		return err
	}
	copy(h.SrcID[:], srcID)
	addr, err := b.ReadString(MaxAddrLen)
	if err != nil {
		return err
	}
	h.SrcAddr = addr
	return nil
}

// EncodedContact is the wire form of a Contact (no LastSeen — that is
// derived locally on receipt).
type EncodedContact struct {
	ID   NodeID
	Addr string
}

func writeContacts(b *wire.Buffer, cs []EncodedContact) {
	b.WriteUvarint(uint64(len(cs)))
	for _, c := range cs {
		b.WriteFixed(c.ID[:])
		b.WriteString(c.Addr)
	}
}

func readContacts(b *wire.Buffer, max int) ([]EncodedContact, error) {
	n, err := b.ReadUvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(max) {
		return nil, fmt.Errorf("dht: contact count %d exceeds max %d", n, max)
	}
	out := make([]EncodedContact, 0, n)
	for range n {
		raw, err := b.ReadFixed(int(IDBits / 8))
		if err != nil {
			return nil, err
		}
		var id NodeID
		copy(id[:], raw)
		addr, err := b.ReadString(MaxAddrLen)
		if err != nil {
			return nil, err
		}
		out = append(out, EncodedContact{ID: id, Addr: addr})
	}
	return out, nil
}

// PingMsg / PongMsg are zero-payload messages besides the header.
type PingMsg struct{ Header Header }
type PongMsg struct{ Header Header }

// FindNodeMsg requests the K nodes closest to Target.
type FindNodeMsg struct {
	Header Header
	Target NodeID
}

// NodesMsg is the response to FindNode (and to FindValue when no value is
// stored locally).
type NodesMsg struct {
	Header   Header
	Contacts []EncodedContact
}

// StoreMsg asks the recipient to store a key/value pair.
type StoreMsg struct {
	Header Header
	Key    NodeID
	Value  []byte
}

// StoreOKMsg acknowledges a Store.
type StoreOKMsg struct{ Header Header }

// FindValueMsg looks up a value by key, falling back to closest-nodes if
// not stored locally.
type FindValueMsg struct {
	Header Header
	Key    NodeID
}

// ValueMsg returns a stored value.
type ValueMsg struct {
	Header Header
	Value  []byte
}

// EncodeMsg serialises any of the DHT message types into a frame.
func EncodeMsg(m any) ([]byte, error) {
	b := wire.NewWriter()
	switch v := m.(type) {
	case *PingMsg:
		v.Header.write(b)
		return wire.EncodeFrame(MsgPing, b.Bytes())
	case *PongMsg:
		v.Header.write(b)
		return wire.EncodeFrame(MsgPong, b.Bytes())
	case *FindNodeMsg:
		v.Header.write(b)
		b.WriteFixed(v.Target[:])
		return wire.EncodeFrame(MsgFindNode, b.Bytes())
	case *NodesMsg:
		v.Header.write(b)
		writeContacts(b, v.Contacts)
		return wire.EncodeFrame(MsgNodes, b.Bytes())
	case *StoreMsg:
		v.Header.write(b)
		b.WriteFixed(v.Key[:])
		b.WriteBytes(v.Value)
		return wire.EncodeFrame(MsgStore, b.Bytes())
	case *StoreOKMsg:
		v.Header.write(b)
		return wire.EncodeFrame(MsgStoreOK, b.Bytes())
	case *FindValueMsg:
		v.Header.write(b)
		b.WriteFixed(v.Key[:])
		return wire.EncodeFrame(MsgFindValue, b.Bytes())
	case *ValueMsg:
		v.Header.write(b)
		b.WriteBytes(v.Value)
		return wire.EncodeFrame(MsgValue, b.Bytes())
	default:
		return nil, fmt.Errorf("dht: cannot encode %T", m)
	}
}

// DecodeMsg parses a wire frame into the matching message type.
// Unknown types return ErrUnknownType.
var ErrUnknownType = errors.New("dht: unknown message type")

func DecodeMsg(frame []byte) (any, error) {
	typ, payload, err := wire.DecodeFrame(frame)
	if err != nil {
		return nil, err
	}
	b := wire.NewBuffer(payload)
	switch typ {
	case MsgPing:
		var m PingMsg
		if err := m.Header.read(b); err != nil {
			return nil, err
		}
		if err := b.SkipUnknownTLVs(); err != nil {
			return nil, err
		}
		return &m, nil
	case MsgPong:
		var m PongMsg
		if err := m.Header.read(b); err != nil {
			return nil, err
		}
		if err := b.SkipUnknownTLVs(); err != nil {
			return nil, err
		}
		return &m, nil
	case MsgFindNode:
		var m FindNodeMsg
		if err := m.Header.read(b); err != nil {
			return nil, err
		}
		raw, err := b.ReadFixed(int(IDBits / 8))
		if err != nil {
			return nil, err
		}
		copy(m.Target[:], raw)
		if err := b.SkipUnknownTLVs(); err != nil {
			return nil, err
		}
		return &m, nil
	case MsgNodes:
		var m NodesMsg
		if err := m.Header.read(b); err != nil {
			return nil, err
		}
		cs, err := readContacts(b, 64)
		if err != nil {
			return nil, err
		}
		m.Contacts = cs
		if err := b.SkipUnknownTLVs(); err != nil {
			return nil, err
		}
		return &m, nil
	case MsgStore:
		var m StoreMsg
		if err := m.Header.read(b); err != nil {
			return nil, err
		}
		raw, err := b.ReadFixed(int(IDBits / 8))
		if err != nil {
			return nil, err
		}
		copy(m.Key[:], raw)
		val, err := b.ReadBytes(MaxStoreValue)
		if err != nil {
			return nil, err
		}
		m.Value = val
		if err := b.SkipUnknownTLVs(); err != nil {
			return nil, err
		}
		return &m, nil
	case MsgStoreOK:
		var m StoreOKMsg
		if err := m.Header.read(b); err != nil {
			return nil, err
		}
		if err := b.SkipUnknownTLVs(); err != nil {
			return nil, err
		}
		return &m, nil
	case MsgFindValue:
		var m FindValueMsg
		if err := m.Header.read(b); err != nil {
			return nil, err
		}
		raw, err := b.ReadFixed(int(IDBits / 8))
		if err != nil {
			return nil, err
		}
		copy(m.Key[:], raw)
		if err := b.SkipUnknownTLVs(); err != nil {
			return nil, err
		}
		return &m, nil
	case MsgValue:
		var m ValueMsg
		if err := m.Header.read(b); err != nil {
			return nil, err
		}
		val, err := b.ReadBytes(MaxStoreValue)
		if err != nil {
			return nil, err
		}
		m.Value = val
		if err := b.SkipUnknownTLVs(); err != nil {
			return nil, err
		}
		return &m, nil
	default:
		return nil, fmt.Errorf("%w: type=%d", ErrUnknownType, typ)
	}
}
