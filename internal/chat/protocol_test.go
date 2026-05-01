package chat_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/chat"
)

func TestMessage_Roundtrip(t *testing.T) {
	t.Parallel()
	m := chat.Message{
		ID:        chat.NewMessageID(),
		Kind:      chat.KindText,
		Timestamp: time.Now().Truncate(time.Nanosecond),
		Body:      []byte("hello"),
	}
	blob, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := chat.Decode(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != m.Kind {
		t.Fatal("kind")
	}
	if !bytes.Equal(got.Body, m.Body) {
		t.Fatal("body")
	}
	if got.ID != m.ID {
		t.Fatal("id")
	}
}

func TestMessage_RejectsTrailingBytes(t *testing.T) {
	t.Parallel()
	m := chat.Message{ID: chat.NewMessageID(), Kind: chat.KindText, Body: []byte("x"), Timestamp: time.Now()}
	blob, _ := m.Encode()
	bad := append(blob, 0xFF)
	if _, err := chat.Decode(bad); !errors.Is(err, chat.ErrDecode) {
		t.Fatalf("err = %v", err)
	}
}

func TestFileOffer_Roundtrip(t *testing.T) {
	t.Parallel()
	o := chat.FileOffer{
		FileID:    chat.NewMessageID(),
		Name:      "kitten.jpg",
		Size:      4096,
		SHA256Hex: "abcd",
	}
	blob := chat.EncodeFileOffer(o)
	got, err := chat.DecodeFileOffer(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got != o {
		t.Fatalf("mismatch: %+v vs %+v", got, o)
	}
}

func TestFileChunk_Roundtrip(t *testing.T) {
	t.Parallel()
	c := chat.FileChunk{FileID: chat.NewMessageID(), Offset: 1024, Data: []byte{1, 2, 3, 4}}
	blob := chat.EncodeFileChunk(c)
	got, err := chat.DecodeFileChunk(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got.Offset != 1024 || !bytes.Equal(got.Data, c.Data) || got.FileID != c.FileID {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestAck_Roundtrip(t *testing.T) {
	t.Parallel()
	a := chat.Ack{ID: chat.NewMessageID()}
	blob := chat.EncodeAck(a)
	got, err := chat.DecodeAck(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID {
		t.Fatal()
	}
}

func FuzzDecodeMessage(f *testing.F) {
	m := chat.Message{ID: chat.NewMessageID(), Kind: chat.KindText, Timestamp: time.Now(), Body: []byte("x")}
	if blob, err := m.Encode(); err == nil {
		f.Add(blob)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = chat.Decode(data)
	})
}
