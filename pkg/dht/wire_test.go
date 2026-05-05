package dht_test

import (
	"bytes"
	"testing"

	"github.com/udisondev/udisend/pkg/dht"
)

func mkHeader() dht.Header {
	tx, err := dht.NewTxID()
	if err != nil {
		panic(err) // tests run with a working RNG; bail loudly otherwise.
	}
	return dht.Header{
		TxID:    tx,
		SrcID:   id("11111111111111111111111111111111"),
		SrcAddr: "127.0.0.1:9001",
	}
}

func TestEncodeDecode_Ping(t *testing.T) {
	t.Parallel()
	in := &dht.PingMsg{Header: mkHeader()}
	blob, err := dht.EncodeMsg(in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dht.DecodeMsg(blob)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := got.(*dht.PingMsg)
	if !ok {
		t.Fatalf("type = %T", got)
	}
	if out.Header.TxID != in.Header.TxID {
		t.Fatal("tx id mismatch")
	}
	if out.Header.SrcAddr != "127.0.0.1:9001" {
		t.Fatal("addr mismatch")
	}
}

func TestEncodeDecode_FindNode(t *testing.T) {
	t.Parallel()
	in := &dht.FindNodeMsg{Header: mkHeader(), Target: id("aabbccddaabbccddaabbccddaabbccdd")}
	blob, _ := dht.EncodeMsg(in)
	got, err := dht.DecodeMsg(blob)
	if err != nil {
		t.Fatal(err)
	}
	out := got.(*dht.FindNodeMsg)
	if out.Target != in.Target {
		t.Fatalf("target mismatch")
	}
}

func TestEncodeDecode_Nodes(t *testing.T) {
	t.Parallel()
	in := &dht.NodesMsg{
		Header: mkHeader(),
		Contacts: []dht.EncodedContact{
			{ID: id("11111111111111111111111111111111"), Addr: "1.2.3.4:1"},
			{ID: id("22222222222222222222222222222222"), Addr: "1.2.3.4:2"},
		},
	}
	blob, _ := dht.EncodeMsg(in)
	got, err := dht.DecodeMsg(blob)
	if err != nil {
		t.Fatal(err)
	}
	out := got.(*dht.NodesMsg)
	if len(out.Contacts) != 2 {
		t.Fatal("contact count")
	}
	if out.Contacts[1].Addr != "1.2.3.4:2" {
		t.Fatal("addr")
	}
}

func TestEncodeDecode_Store(t *testing.T) {
	t.Parallel()
	val := []byte("presence-blob")
	in := &dht.StoreMsg{Header: mkHeader(), Key: id("33333333333333333333333333333333"), Value: val}
	blob, _ := dht.EncodeMsg(in)
	got, err := dht.DecodeMsg(blob)
	if err != nil {
		t.Fatal(err)
	}
	out := got.(*dht.StoreMsg)
	if !bytes.Equal(out.Value, val) {
		t.Fatal("value")
	}
	if out.Key != in.Key {
		t.Fatal("key")
	}
}

func TestDecode_RejectsTrailing(t *testing.T) {
	t.Parallel()
	in := &dht.PingMsg{Header: mkHeader()}
	blob, _ := dht.EncodeMsg(in)
	bad := append(blob[:len(blob)-1:len(blob)-1], 0x00, 0xFF)
	if _, err := dht.DecodeMsg(bad); err == nil {
		t.Fatal("trailing bytes accepted")
	}
}

func FuzzDecodeMsg(f *testing.F) {
	in := &dht.PingMsg{Header: mkHeader()}
	if blob, err := dht.EncodeMsg(in); err == nil {
		f.Add(blob)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = dht.DecodeMsg(data)
	})
}
