package dht

import (
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/udisondev/udisend/pkg/wire"
)

// Message type codes used inside wire frames.
const (
	MsgPing      byte = 0x01
	MsgPong      byte = 0x02
	MsgFindNode  byte = 0x03
	MsgNodes     byte = 0x04
	MsgStore     byte = 0x05
	MsgStoreOK   byte = 0x06
	MsgFindValue byte = 0x07
	MsgValue     byte = 0x08
	// MsgRelay is used by pkg/signaling for opaque payload routing through
	// DHT nodes. We define the constant here so the demux table is in one
	// place; the payload format is defined in pkg/signaling.
	MsgRelay byte = 0x10
)

// TxIDSize is the length of a transaction identifier.
const TxIDSize = 16

// TxID is a random transaction identifier matching requests to responses.
type TxID [TxIDSize]byte

// NewTxID returns a fresh random TxID. Panics if the OS RNG fails — a
// zero TxID would cause request/response collision (multiple in-flight
// requests sharing a key) and crypto-randomness failure is the only
// thing that could produce one. crypto/rand is documented as panic-on-
// failure-equivalent (process should crash), so propagating that
// outcome here is correct.
func NewTxID() TxID {
	var t TxID
	if _, err := rand.Read(t[:]); err != nil {
		panic("dht: crypto/rand: " + err.Error())
	}
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
	if err := b.ReadFixedInto(h.TxID[:]); err != nil {
		return err
	}
	if err := b.ReadFixedInto(h.SrcID[:]); err != nil {
		return err
	}
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
	out := make([]EncodedContact, n)
	for i := range out {
		if err := b.ReadFixedInto(out[i].ID[:]); err != nil {
			return nil, err
		}
		addr, err := b.ReadString(MaxAddrLen)
		if err != nil {
			return nil, err
		}
		out[i].Addr = addr
	}
	return out, nil
}

// PingMsg probes a peer's liveness; the response is a PongMsg.
type PingMsg struct{ Header Header }

// PongMsg is the response to a PingMsg.
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

// EncodeMsg serialises any of the DHT message types into a frame. Uses
// wire.NewFrameWriter so the version/type/length prefix and the body live
// in one allocation — halves the alloc count and avoids the body→frame
// copy of the previous EncodeFrame two-step.
func EncodeMsg(m any) ([]byte, error) {
	switch v := m.(type) {
	case *PingMsg:
		b := wire.NewFrameWriter(headerHint(&v.Header))
		v.Header.write(b)
		return wire.FinishFrame(b, MsgPing)
	case *PongMsg:
		b := wire.NewFrameWriter(headerHint(&v.Header))
		v.Header.write(b)
		return wire.FinishFrame(b, MsgPong)
	case *FindNodeMsg:
		b := wire.NewFrameWriter(headerHint(&v.Header) + int(IDBits/8))
		v.Header.write(b)
		b.WriteFixed(v.Target[:])
		return wire.FinishFrame(b, MsgFindNode)
	case *NodesMsg:
		b := wire.NewFrameWriter(headerHint(&v.Header) + 1 + len(v.Contacts)*(int(IDBits/8)+1+32))
		v.Header.write(b)
		writeContacts(b, v.Contacts)
		return wire.FinishFrame(b, MsgNodes)
	case *StoreMsg:
		b := wire.NewFrameWriter(headerHint(&v.Header) + int(IDBits/8) + 4 + len(v.Value))
		v.Header.write(b)
		b.WriteFixed(v.Key[:])
		b.WriteBytes(v.Value)
		return wire.FinishFrame(b, MsgStore)
	case *StoreOKMsg:
		b := wire.NewFrameWriter(headerHint(&v.Header))
		v.Header.write(b)
		return wire.FinishFrame(b, MsgStoreOK)
	case *FindValueMsg:
		b := wire.NewFrameWriter(headerHint(&v.Header) + int(IDBits/8))
		v.Header.write(b)
		b.WriteFixed(v.Key[:])
		return wire.FinishFrame(b, MsgFindValue)
	case *ValueMsg:
		b := wire.NewFrameWriter(headerHint(&v.Header) + 4 + len(v.Value))
		v.Header.write(b)
		b.WriteBytes(v.Value)
		return wire.FinishFrame(b, MsgValue)
	default:
		return nil, fmt.Errorf("dht: cannot encode %T", m)
	}
}

// headerHint returns the byte size of an encoded Header — TxID || SrcID ||
// uvarint(addrLen) || addr — for sizing the frame buffer at encode time.
func headerHint(h *Header) int {
	return TxIDSize + int(IDBits/8) + 1 + len(h.SrcAddr)
}

// ErrUnknownType is returned by DecodeMsg for frame types the DHT does not
// recognise. Callers can use errors.Is to distinguish it from malformed
// frames (which return wire.ErrShortBuffer / similar).
var ErrUnknownType = errors.New("dht: unknown message type")

// DecodeMsg parses a wire frame into the matching message type.
// Unknown types return ErrUnknownType.
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
		if err := b.ReadFixedInto(m.Target[:]); err != nil {
			return nil, err
		}
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
		if err := b.ReadFixedInto(m.Key[:]); err != nil {
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
		if err := b.ReadFixedInto(m.Key[:]); err != nil {
			return nil, err
		}
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
