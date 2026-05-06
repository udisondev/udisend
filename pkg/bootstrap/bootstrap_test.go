package bootstrap_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/udisondev/udisend/pkg/bootstrap"
)

type fakeResolver struct {
	hosts map[string][]string
	err   error
}

func (f fakeResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	if ips, ok := f.hosts[host]; ok {
		return ips, nil
	}

	return nil, errors.New("nxdomain")
}

func TestDefaults_CommunityListPreserved(t *testing.T) {
	t.Parallel()

	got := bootstrap.Defaults(context.Background(), bootstrap.Config{
		CommunityList: []string{"a.example:9000", "b.example:9000"},
		DNSSeeds:      []string{},
		Resolver:      fakeResolver{},
	})

	want := []string{"a.example:9000", "b.example:9000"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDefaults_DedupAcrossSources(t *testing.T) {
	t.Parallel()

	got := bootstrap.Defaults(context.Background(), bootstrap.Config{
		CommunityList: []string{"a.example:9000", "1.2.3.4:9000"},
		DNSSeeds:      []string{"seed.example"},
		Port:          9000,
		Resolver: fakeResolver{
			hosts: map[string][]string{
				"seed.example": {"1.2.3.4", "5.6.7.8"},
			},
		},
	})

	want := []string{"a.example:9000", "1.2.3.4:9000", "5.6.7.8:9000"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDefaults_DNSFailureSilentlySkipped(t *testing.T) {
	t.Parallel()

	got := bootstrap.Defaults(context.Background(), bootstrap.Config{
		CommunityList: []string{"a.example:9000"},
		DNSSeeds:      []string{"broken.example"},
		Resolver:      fakeResolver{err: errors.New("dns down")},
	})

	want := []string{"a.example:9000"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDefaults_DefaultPortApplied(t *testing.T) {
	t.Parallel()

	got := bootstrap.Defaults(context.Background(), bootstrap.Config{
		CommunityList: []string{},
		DNSSeeds:      []string{"seed.example"},
		Resolver: fakeResolver{
			hosts: map[string][]string{
				"seed.example": {"8.8.8.8"},
			},
		},
	})

	want := []string{"8.8.8.8:9000"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestDefaults_FiltersUnroutableDNSResults exercises the DNS-spoof
// defence: a malicious resolver (or local-network attacker) must not be
// able to redirect a fresh client at loopback / RFC1918 / link-local /
// documentation IPs by poisoning the DNS-seed lookup.
func TestDefaults_FiltersUnroutableDNSResults(t *testing.T) {
	t.Parallel()

	got := bootstrap.Defaults(context.Background(), bootstrap.Config{
		CommunityList: []string{},
		DNSSeeds:      []string{"poisoned.example"},
		Resolver: fakeResolver{
			hosts: map[string][]string{
				"poisoned.example": {
					"127.0.0.1",          // loopback
					"10.0.0.1",           // RFC1918
					"169.254.1.1",        // link-local
					"192.168.1.1",        // RFC1918
					"203.0.113.1",        // TEST-NET-3 (RFC 5737)
					"::1",                // IPv6 loopback
					"fe80::1",            // IPv6 link-local
					"2001:db8::1",        // documentation (RFC 3849)
					"8.8.8.8",            // legitimate (kept)
				},
			},
		},
	})

	want := []string{"8.8.8.8:9000"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDefaults_RespectsContextTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got := bootstrap.Defaults(ctx, bootstrap.Config{
		CommunityList: []string{"a.example:9000"},
		DNSSeeds:      []string{"seed.example"},
		Resolver:      blockingResolver{},
		Timeout:       10 * time.Millisecond,
	})

	want := []string{"a.example:9000"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

type blockingResolver struct{}

func (blockingResolver) LookupHost(ctx context.Context, _ string) ([]string, error) {
	<-ctx.Done()

	return nil, ctx.Err()
}

func TestDefaults_TrimsWhitespace(t *testing.T) {
	t.Parallel()

	got := bootstrap.Defaults(context.Background(), bootstrap.Config{
		CommunityList: []string{"  a.example:9000  ", "", "\t"},
		DNSSeeds:      []string{},
		Resolver:      fakeResolver{},
	})

	want := []string{"a.example:9000"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
