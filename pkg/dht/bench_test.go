package dht_test

import (
	"crypto/rand"
	"net"
	"testing"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
)

// benchHeader builds a representative Header without hitting the RNG inside
// the timed loop.
func benchHeader() dht.Header {
	return dht.Header{
		TxID:    dht.NewTxID(),
		SrcID:   id("aabbccddaabbccddaabbccddaabbccdd"),
		SrcAddr: "127.0.0.1:9001",
	}
}

func BenchmarkEncodeMsg_Ping(b *testing.B) {
	msg := &dht.PingMsg{Header: benchHeader()}
	b.ReportAllocs()
	for b.Loop() {
		blob, err := dht.EncodeMsg(msg)
		if err != nil {
			b.Fatal(err)
		}
		_ = blob
	}
}

func BenchmarkEncodeMsg_FindNode(b *testing.B) {
	msg := &dht.FindNodeMsg{
		Header: benchHeader(),
		Target: id("0123456789abcdef0123456789abcdef"),
	}
	b.ReportAllocs()
	for b.Loop() {
		blob, err := dht.EncodeMsg(msg)
		if err != nil {
			b.Fatal(err)
		}
		_ = blob
	}
}

func BenchmarkEncodeMsg_Nodes(b *testing.B) {
	contacts := make([]dht.EncodedContact, 20)
	for i := range contacts {
		contacts[i] = dht.EncodedContact{
			ID:   id("0123456789abcdef0123456789abcdef"),
			Addr: "127.0.0.1:9001",
		}
	}
	msg := &dht.NodesMsg{Header: benchHeader(), Contacts: contacts}
	b.ReportAllocs()
	for b.Loop() {
		blob, err := dht.EncodeMsg(msg)
		if err != nil {
			b.Fatal(err)
		}
		_ = blob
	}
}

func BenchmarkEncodeMsg_Store(b *testing.B) {
	value := make([]byte, 256)
	_, _ = rand.Read(value)
	msg := &dht.StoreMsg{
		Header: benchHeader(),
		Key:    id("0123456789abcdef0123456789abcdef"),
		Value:  value,
	}
	b.ReportAllocs()
	for b.Loop() {
		blob, err := dht.EncodeMsg(msg)
		if err != nil {
			b.Fatal(err)
		}
		_ = blob
	}
}

func BenchmarkDecodeMsg_Ping(b *testing.B) {
	msg := &dht.PingMsg{Header: benchHeader()}
	blob, err := dht.EncodeMsg(msg)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		out, err := dht.DecodeMsg(blob)
		if err != nil {
			b.Fatal(err)
		}
		_ = out
	}
}

func BenchmarkDecodeMsg_FindNode(b *testing.B) {
	msg := &dht.FindNodeMsg{
		Header: benchHeader(),
		Target: id("0123456789abcdef0123456789abcdef"),
	}
	blob, err := dht.EncodeMsg(msg)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		out, err := dht.DecodeMsg(blob)
		if err != nil {
			b.Fatal(err)
		}
		_ = out
	}
}

func BenchmarkDecodeMsg_Nodes(b *testing.B) {
	contacts := make([]dht.EncodedContact, 20)
	for i := range contacts {
		contacts[i] = dht.EncodedContact{
			ID:   id("0123456789abcdef0123456789abcdef"),
			Addr: "127.0.0.1:9001",
		}
	}
	msg := &dht.NodesMsg{Header: benchHeader(), Contacts: contacts}
	blob, err := dht.EncodeMsg(msg)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		out, err := dht.DecodeMsg(blob)
		if err != nil {
			b.Fatal(err)
		}
		_ = out
	}
}

func BenchmarkDecodeMsg_Store(b *testing.B) {
	value := make([]byte, 256)
	_, _ = rand.Read(value)
	msg := &dht.StoreMsg{
		Header: benchHeader(),
		Key:    id("0123456789abcdef0123456789abcdef"),
		Value:  value,
	}
	blob, err := dht.EncodeMsg(msg)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		out, err := dht.DecodeMsg(blob)
		if err != nil {
			b.Fatal(err)
		}
		_ = out
	}
}

// BenchmarkRoutingTable_Closest exercises the Kademlia routing-table
// `Closest` lookup that fires on every FIND_NODE / FIND_VALUE.
func BenchmarkRoutingTable_Closest(b *testing.B) {
	self := id("00000000000000000000000000000001")
	rt := dht.NewRoutingTable(self, dht.DefaultK)
	for range 256 {
		var raw [identity.HashSize]byte
		_, _ = rand.Read(raw[:])
		var nid dht.NodeID
		copy(nid[:], raw[:])
		rt.Add(dht.Contact{ID: nid, Addr: dummyAddr("127.0.0.1:9000")})
	}
	target := id("0123456789abcdef0123456789abcdef")
	b.ReportAllocs()
	for b.Loop() {
		_ = rt.Closest(target, dht.DefaultK)
	}
}

// dummyAddr lets us put a non-nil net.Addr on Contact without resolving.
type dummyAddr string

func (d dummyAddr) Network() string { return "udp" }
func (d dummyAddr) String() string  { return string(d) }

// Compile-time assertion that dummyAddr satisfies net.Addr.
var _ net.Addr = dummyAddr("")
