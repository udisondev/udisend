package stun_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	pionstun "github.com/pion/stun/v3"
	udsstun "github.com/udisondev/udisend/pkg/stun"
)

func TestServer_BindingResponse(t *testing.T) {
	t.Parallel()
	srv, err := udsstun.Listen("127.0.0.1:0")
	if err != nil {
		t.Skipf("UDP not available: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	go func() { _ = srv.Run(ctx) }()
	defer cancel()

	client, err := net.DialUDP("udp", nil, srv.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	msg, err := pionstun.Build(pionstun.TransactionID, pionstun.BindingRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write(msg.Raw); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resp := &pionstun.Message{Raw: buf[:n]}
	if err := resp.Decode(); err != nil {
		t.Fatal(err)
	}
	var xor pionstun.XORMappedAddress
	if err := xor.GetFrom(resp); err != nil && !errors.Is(err, pionstun.ErrAttributeNotFound) {
		t.Fatal(err)
	}
	if xor.IP == nil {
		t.Fatal("no XORMappedAddress in response")
	}
}
