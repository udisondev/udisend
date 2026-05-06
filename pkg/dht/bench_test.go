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
	tx, err := dht.NewTxID()
	if err != nil {
		panic(err)
	}
	return dht.Header{
		TxID:    tx,
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

// BenchmarkRoutingTable_ClosestEncode simulates the full FIND_NODE reply
// path: look up K closest contacts and encode them into a NodesMsg frame.
// This is what happens on every FIND_NODE / FIND_VALUE the network node
// serves.
func BenchmarkRoutingTable_ClosestEncode(b *testing.B) {
	self := id("00000000000000000000000000000001")
	rt := dht.NewRoutingTable(self, dht.DefaultK)
	for i := range 256 {
		var raw [identity.HashSize]byte
		_, _ = rand.Read(raw[:])
		var nid dht.NodeID
		copy(nid[:], raw[:])
		addrStr := "192.0.2." + itoa(i&255) + ":9000"
		rt.Add(dht.Contact{
			ID:   nid,
			Addr: dummyAddr(addrStr),
		})
	}
	target := id("0123456789abcdef0123456789abcdef")
	hdr := benchHeader()
	b.ReportAllocs()
	for b.Loop() {
		closest := rt.Closest(target, dht.DefaultK)
		// Mirror dht.encodeContacts (unexported) inside the bench so we
		// measure the same alloc shape the real handler pays.
		encoded := make([]dht.EncodedContact, len(closest))
		for j, c := range closest {
			encoded[j] = dht.EncodedContact{ID: c.ID, Addr: c.Addr.String()}
		}
		_, err := dht.EncodeMsg(&dht.NodesMsg{Header: hdr, Contacts: encoded})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// itoa is a tiny stack-allocating itoa for benchmarks; the stdlib version
// allocates.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
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
