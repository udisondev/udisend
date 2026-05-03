// Package bootstrap supplies fallback peer addresses used by both
// cmd/messenger and cmd/network when the operator did not pass an
// explicit --bootstrap flag and the local seen-peers cache is empty.
//
// The package solves design.md §7 ("Bootstrap-список / DNS-seeds /
// сеть самовоспроизводится"): it keeps a curated community list of
// host:port endpoints and resolves an additional set of well-known
// DNS-seed names whose A/AAAA records point to currently-up nodes.
// Both inputs are merged and deduplicated.
//
// The defaults are intentionally empty in this open-source release —
// publishing fake addresses would harden nothing. Operators of a public
// network are expected to fork CommunityList and DNSSeeds with their
// own values, or pass them in through Config.
package bootstrap

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"
)

// CommunityList is the curated, in-binary list of host:port endpoints
// shipped with the build. design.md §7 calls for "20-30 bootstrap-адресов
// от разных людей в разных юрисдикциях"; this slice is the place to put
// them. Empty in upstream — populate in your fork or build pipeline.
//
// var ldflag overrides are intentional: a release pipeline can splice
// addresses in without recompiling sources by passing
// `-ldflags '-X github.com/udisondev/udisend/pkg/bootstrap.ldflagList=a:1,b:2'`.
var CommunityList = []string{}

// ldflagList is the override slot for build pipelines (comma-separated
// host:port). Parsed lazily on first call to Defaults.
var ldflagList = ""

// DNSSeeds is the list of hostnames whose A/AAAA records point to live
// network nodes. The well-known port (DefaultPort) is appended to every
// resolved address. Empty in upstream — fill in for a public deployment.
var DNSSeeds = []string{}

// DefaultPort is the UDP port assumed for DNS-resolved seeds.
const DefaultPort = 9000

// DefaultDNSTimeout caps each DNS lookup. The bootstrap process is
// best-effort — failing one seed should not stall startup.
const DefaultDNSTimeout = 3 * time.Second

// Resolver is the subset of net.Resolver behaviour we depend on. Tests
// inject a fake here.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Config tunes Defaults / Resolve.
type Config struct {
	// CommunityList overrides the package-level CommunityList. If nil,
	// the package default (plus ldflag override) is used.
	CommunityList []string

	// DNSSeeds overrides the package-level DNSSeeds.
	DNSSeeds []string

	// Port is the UDP port appended to DNS-resolved hosts. Zero means
	// DefaultPort.
	Port int

	// Resolver is the DNS resolver used for DNSSeeds. Zero means
	// net.DefaultResolver.
	Resolver Resolver

	// Timeout caps each DNS lookup. Zero means DefaultDNSTimeout.
	Timeout time.Duration
}

// Defaults returns the merged, deduplicated bootstrap address list:
// every entry from cfg.CommunityList plus every A/AAAA record from
// cfg.DNSSeeds (each formatted as host:port). Order is preserved
// from input (community first, then DNS seeds in declaration order),
// duplicates dropped on second occurrence.
//
// Errors per individual DNS lookup are silently ignored — a single
// resolvable seed is enough.
func Defaults(ctx context.Context, cfg Config) []string {
	community := cfg.CommunityList
	if community == nil {
		community = communityWithLDFlag()
	}

	seeds := cfg.DNSSeeds
	if seeds == nil {
		seeds = DNSSeeds
	}

	port := cfg.Port
	if port == 0 {
		port = DefaultPort
	}

	resolver := cfg.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultDNSTimeout
	}

	seen := make(map[string]struct{}, len(community)+len(seeds)*2)
	out := make([]string, 0, len(community)+len(seeds)*2)
	add := func(addr string) {
		if addr == "" {
			return
		}
		if _, ok := seen[addr]; ok {
			return
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}

	for _, addr := range community {
		add(strings.TrimSpace(addr))
	}

	for _, host := range seeds {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, timeout)
		ips, err := resolver.LookupHost(lctx, host)
		cancel()
		if err != nil {
			continue
		}
		for _, ip := range ips {
			add(net.JoinHostPort(ip, strconv.Itoa(port)))
		}
	}

	return out
}

// communityWithLDFlag returns CommunityList augmented with the
// ldflag-injected list, if any. The ldflag list is comma-separated
// host:port entries.
func communityWithLDFlag() []string {
	if ldflagList == "" {
		return CommunityList
	}

	extras := strings.Split(ldflagList, ",")
	out := make([]string, 0, len(CommunityList)+len(extras))
	out = append(out, CommunityList...)
	for _, e := range extras {
		e = strings.TrimSpace(e)
		if e != "" {
			out = append(out, e)
		}
	}

	return out
}
