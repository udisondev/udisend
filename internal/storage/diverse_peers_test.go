package storage_test

import (
	"slices"
	"testing"
)

func TestSeenPeersDiverse_PrefersDistinctSubnets(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	// 4 peers across 2 /24 subnets. Diverse(limit=2) must return one from
	// each subnet, not both from the same.
	peers := []string{
		"203.0.113.10:9000",
		"203.0.113.11:9000",
		"198.51.100.4:9000",
		"198.51.100.5:9000",
	}
	for _, addr := range peers {
		if err := s.RecordSeenPeer(ctx, addr); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.SeenPeersDiverse(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d peers, want 2", len(got))
	}
	subnets := map[string]bool{}
	for _, addr := range got {
		// Crude /24 extraction for the assertion only.
		// We don't reach into subnetKey so the test stays a black-box check.
		switch {
		case slices.Contains([]string{"203.0.113.10:9000", "203.0.113.11:9000"}, addr):
			subnets["203.0.113"] = true
		case slices.Contains([]string{"198.51.100.4:9000", "198.51.100.5:9000"}, addr):
			subnets["198.51.100"] = true
		}
	}
	if len(subnets) != 2 {
		t.Fatalf("expected 2 distinct subnets, got %v from %v", subnets, got)
	}
}

func TestSeenPeersDiverse_HandlesIPv6AndNonIP(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()
	// Two IPv6 addresses in same /64 → only one survives diverse filter.
	if err := s.RecordSeenPeer(ctx, "[2001:db8::1]:9000"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSeenPeer(ctx, "[2001:db8::2]:9000"); err != nil {
		t.Fatal(err)
	}
	// A "memory transport" style address, which the filter should keep
	// independently (no IP prefix collision).
	if err := s.RecordSeenPeer(ctx, "mem:42"); err != nil {
		t.Fatal(err)
	}
	got, err := s.SeenPeersDiverse(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 (one IPv6 prefix + one mem), got %d: %v", len(got), got)
	}
}
