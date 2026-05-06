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
	const secret = "test-secret"
	srv, err := udsturn.NewServer(udsturn.Config{
		PublicIP:     "127.0.0.1",
		ListenAddr:   "127.0.0.1:0",
		SharedSecret: secret,
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

	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	username, password := udsturn.EphemeralCredential(secret, "alice", time.Now().Add(10*time.Minute))
	client, err := pionturn.NewClient(&pionturn.ClientConfig{
		STUNServerAddr: srv.LocalAddr().String(),
		TURNServerAddr: srv.LocalAddr().String(),
		Conn:           conn,
		Username:       username,
		Password:       password,
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

	if _, ok := relay.LocalAddr().(*net.UDPAddr); !ok {
		t.Fatalf("relay address type %T", relay.LocalAddr())
	}
}

// Phase 11.7: Config.Realm overrides DefaultRealm. Verifies a custom
// realm is plumbed through pion's auth challenge — clients keying on
// the wrong realm get refused.
func TestServer_CustomRealm_Allocate(t *testing.T) {
	t.Parallel()
	const secret = "test-secret"
	const customRealm = "example.org"

	srv, err := udsturn.NewServer(udsturn.Config{
		PublicIP:     "127.0.0.1",
		ListenAddr:   "127.0.0.1:0",
		SharedSecret: secret,
		Realm:        customRealm,
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

	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	username, password := udsturn.EphemeralCredential(secret, "alice", time.Now().Add(10*time.Minute))
	client, err := pionturn.NewClient(&pionturn.ClientConfig{
		STUNServerAddr: srv.LocalAddr().String(),
		TURNServerAddr: srv.LocalAddr().String(),
		Conn:           conn,
		Username:       username,
		Password:       password,
		Realm:          customRealm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := client.Listen(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Allocate(); err != nil {
		t.Fatalf("allocate with custom realm: %v", err)
	}
}

func TestServer_RejectsExpiredCredential(t *testing.T) {
	t.Parallel()
	const secret = "test-secret"
	srv, err := udsturn.NewServer(udsturn.Config{
		PublicIP:     "127.0.0.1",
		ListenAddr:   "127.0.0.1:0",
		SharedSecret: secret,
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

	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Username with expiry in the past — auth must fail.
	username, password := udsturn.EphemeralCredential(secret, "mallory", time.Now().Add(-1*time.Hour))
	client, err := pionturn.NewClient(&pionturn.ClientConfig{
		STUNServerAddr: srv.LocalAddr().String(),
		TURNServerAddr: srv.LocalAddr().String(),
		Conn:           conn,
		Username:       username,
		Password:       password,
		Realm:          udsturn.Realm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	if err := client.Listen(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Allocate(); err == nil {
		t.Fatal("expected Allocate to fail for expired credential")
	}
}
