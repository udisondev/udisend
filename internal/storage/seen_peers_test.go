package storage_test

import (
	"slices"
	"testing"
)

func TestSeenPeers_RecordAndList(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	for _, addr := range []string{"127.0.0.1:9001", "127.0.0.1:9002", "127.0.0.1:9001"} {
		if err := s.RecordSeenPeer(ctx, addr); err != nil {
			t.Fatalf("record %q: %v", addr, err)
		}
	}

	got, err := s.SeenPeers(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.1:9001", "127.0.0.1:9002"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !slices.Contains(got, "127.0.0.1:9001") || !slices.Contains(got, "127.0.0.1:9002") {
		t.Fatalf("missing entries: %v", got)
	}
}

func TestSeenPeers_LimitOrdering(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	// Insert two; the second is most-recent.
	if err := s.RecordSeenPeer(ctx, "older"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSeenPeer(ctx, "newer"); err != nil {
		t.Fatal(err)
	}

	one, err := s.SeenPeers(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0] != "newer" {
		t.Fatalf("expected most-recent first; got %v", one)
	}
}

func TestSeenPeers_Forget(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()
	if err := s.RecordSeenPeer(ctx, "127.0.0.1:9001"); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetSeenPeer(ctx, "127.0.0.1:9001"); err != nil {
		t.Fatal(err)
	}
	got, err := s.SeenPeers(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty after forget, got %v", got)
	}
}
