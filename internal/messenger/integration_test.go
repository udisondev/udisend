//go:build integration

package messenger_test

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/messenger"
	"github.com/udisondev/udisend/internal/network"
	"github.com/udisondev/udisend/pkg/identity"
)

// TestMessenger_E2E_TextOverNetworkNode wires a network node + two
// messengers via real UDP loopback and exchanges a text message
// end-to-end (DHT discovery → signaling → WebRTC DataChannel → ACK).
//
// Build tag `integration` keeps it out of the default test run because
// real UDP, DHT bootstrap and WebRTC handshake make it slow.
func TestMessenger_E2E_TextOverNetworkNode(t *testing.T) {
	netID, _ := identity.Generate(rand.Reader)
	aliceID, _ := identity.Generate(rand.Reader)
	bobID, _ := identity.Generate(rand.Reader)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	netNode, err := network.Open(ctx, network.Config{
		Identity: netID,
		Listen:   "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = netNode.Run(ctx) }()
	defer cancel()

	dir := t.TempDir()
	alice, err := messenger.Open(ctx, messenger.Config{
		Identity:   aliceID,
		Listen:     "127.0.0.1:0",
		Bootstrap:  []string{netNode.LocalAddress()},
		StorageDir: filepath.Join(dir, "alice"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	go alice.Run(ctx)

	bob, err := messenger.Open(ctx, messenger.Config{
		Identity:   bobID,
		Listen:     "127.0.0.1:0",
		Bootstrap:  []string{netNode.LocalAddress()},
		StorageDir: filepath.Join(dir, "bob"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	go bob.Run(ctx)

	gotMsg := make(chan string, 1)
	bob.SetListener(messenger.Listener{
		OnMessage: func(e messenger.EventMessage) {
			select {
			case gotMsg <- e.Body:
			default:
			}
		},
	})

	// Wait for presence records to land in the DHT (publisher refresh).
	time.Sleep(2 * time.Second)

	// Alice adds Bob as a contact via presence resolution.
	addCtx, cancelAdd := context.WithTimeout(ctx, 6*time.Second)
	defer cancelAdd()
	if err := alice.AddContact(addCtx, bob.Identity().Public().DestinationHash(), "bob"); err != nil {
		t.Fatalf("alice.AddContact: %v", err)
	}
	if err := bob.AddContact(addCtx, alice.Identity().Public().DestinationHash(), "alice"); err != nil {
		t.Fatalf("bob.AddContact: %v", err)
	}

	sendCtx, cancelSend := context.WithTimeout(ctx, 30*time.Second)
	defer cancelSend()
	if err := alice.SendText(sendCtx, bob.Identity().Public().DestinationHash(), "hello bob"); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case got := <-gotMsg:
		if got != "hello bob" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("bob never received the message")
	}
}
