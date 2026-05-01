package presence

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/udisondev/udisend/pkg/dht"
	"github.com/udisondev/udisend/pkg/identity"
)

// DHT is the subset of *dht.Node used by the publisher / resolver. Defined
// as an interface so tests can substitute a fake.
type DHT interface {
	PutValue(ctx context.Context, key dht.NodeID, value []byte) error
	LookupValue(ctx context.Context, key dht.NodeID) ([]byte, []dht.Contact, error)
}

// Publisher periodically signs and pushes a presence record into the DHT.
type Publisher struct {
	id           *identity.Identity
	addr         string
	capabilities Capability
	ttl          time.Duration
	refresh      time.Duration
	dht          DHT
	log          *slog.Logger
}

// PublisherConfig configures NewPublisher.
type PublisherConfig struct {
	Identity     *identity.Identity
	Address      string
	Capabilities Capability
	TTL          time.Duration // record validity window
	Refresh      time.Duration // re-publish interval; defaults to TTL/2
	DHT          DHT
	Logger       *slog.Logger
}

// NewPublisher returns a Publisher ready to be Run.
func NewPublisher(cfg PublisherConfig) *Publisher {
	if cfg.TTL == 0 {
		cfg.TTL = dht.DefaultPresenceTTL
	}
	if cfg.Refresh == 0 {
		cfg.Refresh = cfg.TTL / 2
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Publisher{
		id:           cfg.Identity,
		addr:         cfg.Address,
		capabilities: cfg.Capabilities,
		ttl:          cfg.TTL,
		refresh:      cfg.Refresh,
		dht:          cfg.DHT,
		log:          cfg.Logger,
	}
}

// Run loops publishing the record until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context) {
	if err := p.publishOnce(ctx); err != nil {
		p.log.Warn("presence: initial publish failed", "err", err)
	}
	t := time.NewTimer(p.refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.publishOnce(ctx); err != nil {
				p.log.Warn("presence: republish failed", "err", err)
			}
			t.Reset(p.refresh)
		}
	}
}

// publishOnce signs a fresh record and stores it in the DHT.
func (p *Publisher) publishOnce(ctx context.Context) error {
	rec := Record{
		Address:      p.addr,
		Capabilities: p.capabilities,
		IssuedAt:     time.Now().UTC(),
	}
	if err := rec.Sign(p.id); err != nil {
		return fmt.Errorf("presence: sign: %w", err)
	}
	blob, err := rec.MarshalBinary()
	if err != nil {
		return fmt.Errorf("presence: marshal: %w", err)
	}
	key := rec.DestinationHash()
	return p.dht.PutValue(ctx, key, blob)
}

// Resolver looks up presence records in the DHT (and a local cache).
type Resolver struct {
	cache *Cache
	dht   DHT
	ttl   time.Duration
	now   func() time.Time
}

// NewResolver returns a resolver backed by the given DHT.
func NewResolver(d DHT, ttl time.Duration) *Resolver {
	return &Resolver{
		cache: NewCache(ttl),
		dht:   d,
		ttl:   ttl,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// Cache exposes the underlying cache. Useful for tests / observability.
func (r *Resolver) Cache() *Cache { return r.cache }

// Lookup fetches the presence record for `peer` from the DHT, validating
// the signature and freshness. The result is cached for the configured
// TTL.
func (r *Resolver) Lookup(ctx context.Context, peer identity.Hash) (*Record, error) {
	if rec, ok := r.cache.Get(r.now(), peer); ok {
		return rec, nil
	}
	val, _, err := r.dht.LookupValue(ctx, peer)
	if err != nil {
		return nil, fmt.Errorf("presence: dht lookup: %w", err)
	}
	if val == nil {
		return nil, ErrNotFound
	}
	var rec Record
	if err := rec.UnmarshalBinary(val); err != nil {
		return nil, err
	}
	if rec.DestinationHash() != peer {
		return nil, ErrIdentityMismatch
	}
	if _, err := r.cache.Put(r.now(), &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ErrNotFound is returned when a presence lookup yields no record.
var ErrNotFound = fmt.Errorf("presence: record not found")
