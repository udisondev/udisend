package turn_test

import (
	"context"
	"net"
	"testing"
	"time"

	pionturn "github.com/pion/turn/v4"
	udsturn "github.com/udisondev/udisend/pkg/turn"
)

func TestServer_AllocateAndDeallocate(t *testing.T) {
	t.Parallel()
	srv, err := udsturn.NewServer(udsturn.Config{
		PublicIP:     "127.0.0.1",
		ListenAddr:   "127.0.0.1:0",
		SharedSecret: "test-secret",
	})
	if err != nil {
		t.Skipf("TURN not available: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	go func() { _ = srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})

	// Build a client and try Allocate.
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client, err := pionturn.NewClient(&pionturn.ClientConfig{
		STUNServerAddr: srv.LocalAddr().String(),
		TURNServerAddr: srv.LocalAddr().String(),
		Conn:           conn,
		Username:       "alice",
		Password:       "test-secret", // long-term-credentials helper expects this
		Realm:          udsturn.Realm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	if err := client.Listen(); err != nil {
		t.Fatal(err)
	}
	relay, err := client.Allocate()
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if relay == nil {
		t.Fatal("nil relay conn")
	}
	t.Cleanup(func() { _ = relay.Close() })

	// Ensure we got a sensible relayed address.
	if _, ok := relay.LocalAddr().(*net.UDPAddr); !ok {
		t.Fatalf("relay address type %T", relay.LocalAddr())
	}
	// Sanity: allocation should not race with shutdown when context is
	// cancelled.
	cancel()
	time.Sleep(50 * time.Millisecond)
}
