package network_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/network"
)

func TestOpen_ListensOnEphemeral(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := mustIdentity(t)
	node, err := network.Open(ctx, network.Config{
		Identity: id,
		Listen:   "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := node.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if node.LocalAddress() == "" {
		t.Fatal("LocalAddress empty")
	}
}

func TestRun_ExitsOnContextCancel(t *testing.T) {
	t.Parallel()
	id := mustIdentity(t)
	ctx, cancel := context.WithCancel(context.Background())
	node, err := network.Open(ctx, network.Config{
		Identity: id,
		Listen:   "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- node.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned err: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func mustIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
