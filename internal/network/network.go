// Package network is the runtime for `cmd/network` — a non-interactive
// node that joins the DHT, acts as a signaling-relay (passive: shares
// the same socket as the DHT so any peer can route messages through it
// while looking up other peers), and optionally serves embedded
// STUN/TURN volunteer endpoints when its host has a routable public IP.
package network

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
	"github.com/udisondev/udisend/pkg/presence"
	"github.com/udisondev/udisend/pkg/signaling"
	"github.com/udisondev/udisend/pkg/stun"
	"github.com/udisondev/udisend/pkg/transport"
	"github.com/udisondev/udisend/pkg/turn"
)

// Config configures a network node.
type Config struct {
	Identity    *identity.Identity
	Listen      string   // UDP listen address; ":0" picks ephemeral
	Bootstrap   []string // peer addresses to seed the routing table
	PublicIP    string   // if non-empty, STUN/TURN are advertised on this IP
	STUNAddr    string   // optional, defaults to ":3478" if PublicIP set
	TURNAddr    string   // optional, defaults to ":3478" (shared with STUN by binding two sockets)
	TURNSecret  string   // shared secret for TURN long-term-credentials
	PresenceTTL time.Duration
	Logger      *slog.Logger
}

// Node bundles every long-lived process inside a network node.
type Node struct {
	cfg Config

	transport *transport.UDPTransport
	dht       *dht.Node
	publisher *presence.Publisher
	signaling *signaling.Service
	stunSrv   *stun.Server
	turnSrv   *turn.Server

	closeOnce sync.Once
	wg        sync.WaitGroup
}

// Open spins up a fresh network node. Call Run to start the loops.
func Open(ctx context.Context, cfg Config) (*Node, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Listen == "" {
		cfg.Listen = ":0"
	}
	if cfg.PresenceTTL == 0 {
		cfg.PresenceTTL = presence.DefaultRecordTTL
	}

	tr, err := transport.ListenUDP(cfg.Listen)
	if err != nil {
		return nil, err
	}

	signalSvc := signaling.NewService(signaling.Config{
		Identity:  cfg.Identity,
		Transport: tr,
		Resolver:  &noopResolver{},
		Logger:    cfg.Logger,
	})

	store := presence.NewRateLimitedStore(dht.NewMemoryStore(nil))
	dhtNode := dht.NewNode(cfg.Identity, tr, store, dht.Config{
		Logger:       cfg.Logger,
		ExtraHandler: signalSvc.HandlePacket,
	})
	signalSvc.SetRouter(newDHTRouter(dhtNode))

	// When the operator passes --public-ip, advertise that IP rather
	// than the bind address. Operators routinely bind to 0.0.0.0 to
	// accept traffic on every interface, but the published record
	// must carry a routable address — otherwise remote peers cannot
	// dial back.
	pubAddr := tr.LocalAddr().String()
	caps := presence.CapCanRelay | presence.CapCanBootstrap
	if cfg.PublicIP != "" {
		caps |= presence.CapPublicIP | presence.CapCanSTUN | presence.CapCanTURN
		if _, port, err := net.SplitHostPort(pubAddr); err == nil {
			pubAddr = net.JoinHostPort(cfg.PublicIP, port)
		}
	}
	publisher := presence.NewPublisher(presence.PublisherConfig{
		Identity:     cfg.Identity,
		Address:      pubAddr,
		Capabilities: caps,
		TTL:          cfg.PresenceTTL,
		DHT:          nodeAdapter{dhtNode},
		Logger:       cfg.Logger,
	})

	n := &Node{
		cfg:       cfg,
		transport: tr,
		dht:       dhtNode,
		publisher: publisher,
		signaling: signalSvc,
	}

	if cfg.PublicIP != "" {
		stunAddr := cfg.STUNAddr
		if stunAddr == "" {
			stunAddr = ":3478"
		}
		ssrv, err := stun.Listen(stunAddr)
		if err != nil {
			cfg.Logger.Warn("network: STUN listen failed; skipping", "err", err)
		} else {
			n.stunSrv = ssrv
		}
		if cfg.TURNSecret != "" {
			turnAddr := cfg.TURNAddr
			if turnAddr == "" {
				turnAddr = ":3479"
			}
			tsrv, err := turn.NewServer(turn.Config{
				PublicIP:     cfg.PublicIP,
				ListenAddr:   turnAddr,
				SharedSecret: cfg.TURNSecret,
			})
			if err != nil {
				cfg.Logger.Warn("network: TURN listen failed; skipping", "err", err)
			} else {
				n.turnSrv = tsrv
			}
		}
	}

	return n, nil
}

// Run starts every loop. Returns when ctx is cancelled.
func (n *Node) Run(ctx context.Context) error {
	n.wg.Go(func() { n.dht.Run(ctx) })
	n.wg.Go(func() { n.publisher.Run(ctx) })

	if n.stunSrv != nil {
		n.wg.Go(func() { _ = n.stunSrv.Run(ctx) })
	}
	if n.turnSrv != nil {
		n.wg.Go(func() { _ = n.turnSrv.Run(ctx) })
	}

	for _, addr := range n.cfg.Bootstrap {
		n.wg.Go(func() {
			peer, err := n.transport.Dial(addr)
			if err != nil {
				n.cfg.Logger.Warn("network: bootstrap parse", "addr", addr, "err", err)
				return
			}
			bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := n.dht.Bootstrap(bctx, peer); err != nil {
				n.cfg.Logger.Warn("network: bootstrap", "addr", addr, "err", err)
			}
		})
	}

	<-ctx.Done()
	n.closeServers()
	n.wg.Wait()
	return nil
}

// LocalAddress returns the bound UDP address — useful in tests / logs.
func (n *Node) LocalAddress() string { return n.transport.LocalAddr().String() }

// Identity returns the node's identity.
func (n *Node) Identity() *identity.Identity { return n.cfg.Identity }

func (n *Node) closeServers() {
	n.closeOnce.Do(func() {
		n.signaling.Close()
		if n.stunSrv != nil {
			_ = n.stunSrv.Close()
		}
		if n.turnSrv != nil {
			_ = n.turnSrv.Close()
		}
		_ = n.transport.Close()
	})
}

// nodeAdapter exposes the subset of dht.Node required by presence.DHT.
type nodeAdapter struct{ n *dht.Node }

func (a nodeAdapter) PutValue(ctx context.Context, key dht.NodeID, value []byte) error {
	return a.n.PutValue(ctx, key, value)
}

func (a nodeAdapter) LookupValue(ctx context.Context, key dht.NodeID) ([]byte, []dht.Contact, error) {
	return a.n.LookupValue(ctx, key)
}

// routingBackend is the subset of dht.Node behaviour dhtRouter needs.
// Interface form lets unit tests substitute a stub.
type routingBackend interface {
	Closest(target identity.Hash, n int) []dht.Contact
	LookupNode(ctx context.Context, target identity.Hash) ([]dht.Contact, error)
}

// dhtRouter satisfies signaling.Router via routingBackend, with a bounded
// iterative-lookup fallback (design.md §4 path requests).
type dhtRouter struct {
	backend routingBackend
	timeout time.Duration
}

const defaultPathLookupTimeout = 1500 * time.Millisecond

func newDHTRouter(node *dht.Node) dhtRouter {
	return dhtRouter{backend: nodeRouting{node}, timeout: defaultPathLookupTimeout}
}

// NextHop resolves a recipient destination hash to the address of the
// next routing hop. Fast path: routing table has `target` directly.
// Slow path: bounded iterative LookupNode tries to discover a route
// through the DHT. If lookup returns nothing, fall back to the closest
// known contact (best-effort hop). Returns false only when both the
// local table is empty and lookup yields nothing.
func (r dhtRouter) NextHop(ctx context.Context, target identity.Hash) (net.Addr, bool) {
	closest := r.backend.Closest(target, 1)
	if len(closest) > 0 && closest[0].ID == target {
		return closest[0].Addr, true
	}
	lctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if found, err := r.backend.LookupNode(lctx, target); err == nil {
		for _, c := range found {
			if c.ID == target {
				return c.Addr, true
			}
		}
		if len(found) > 0 {
			return found[0].Addr, true
		}
	}
	if len(closest) > 0 {
		return closest[0].Addr, true
	}
	return nil, false
}

// nodeRouting wraps *dht.Node into routingBackend.
type nodeRouting struct{ n *dht.Node }

func (b nodeRouting) Closest(target identity.Hash, n int) []dht.Contact {
	return b.n.Table().Closest(target, n)
}

func (b nodeRouting) LookupNode(ctx context.Context, target identity.Hash) ([]dht.Contact, error) {
	return b.n.LookupNode(ctx, target)
}

// noopResolver satisfies signaling.AddressResolver for a node that doesn't
// initiate signaling sessions itself — only relays them.
type noopResolver struct{}

func (noopResolver) Lookup(_ context.Context, _ identity.Hash) (*presence.Record, error) {
	return nil, errNoLookups
}

var errNoLookups = errors.New("network: this node does not initiate signaling lookups")
