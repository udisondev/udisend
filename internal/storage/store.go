// Package storage is the SQLite layer for the messenger client. It owns
// contacts (TOFU + verification), message history and the outbox. Pure-Go
// driver (modernc.org/sqlite) keeps the messenger binary CGO-free.
package storage

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"time"

	_ "modernc.org/sqlite"

	"github.com/udisondev/udisend/pkg/identity"
)

//go:embed schema.sql
var schema string

// Store is the high-level wrapper around a *sql.DB that the application
// layers see.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database at path and ensures the schema.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: ping: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// Contact is the persisted view of a peer.
type Contact struct {
	Hash        identity.Hash
	Public      identity.PublicIdentity
	Alias       string
	Fingerprint string
	Verified    bool
	AddedAt     time.Time
}

// UpsertContact creates or updates a contact. If the contact already
// exists with a different ed_pub/x_pub, returns ErrFingerprintChanged —
// the caller (TOFU layer) decides what to do.
func (s *Store) UpsertContact(ctx context.Context, c Contact) error {
	row := s.db.QueryRowContext(ctx, "SELECT ed_pub, x_pub FROM contacts WHERE destination_hash = ?", c.Hash.String())
	var existingEd, existingX []byte
	err := row.Scan(&existingEd, &existingX)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO contacts (destination_hash, ed_pub, x_pub, alias, fingerprint, verified, added_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, c.Hash.String(), []byte(c.Public.EdPub), c.Public.XPub[:], c.Alias, c.Fingerprint, boolInt(c.Verified), c.AddedAt.Unix())
		return err
	case err != nil:
		return err
	}
	// Existing — fingerprint check.
	xSlice := c.Public.XPub
	if !bytes.Equal(existingEd, []byte(c.Public.EdPub)) || !bytes.Equal(existingX, xSlice[:]) {
		return ErrFingerprintChanged
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE contacts SET alias = ?, fingerprint = ?, verified = ? WHERE destination_hash = ?
	`, c.Alias, c.Fingerprint, boolInt(c.Verified), c.Hash.String())
	return err
}

// ErrFingerprintChanged is returned when the stored ed_pub/x_pub differs
// from the supplied ones.
var ErrFingerprintChanged = errors.New("storage: fingerprint changed for known contact")

// GetContact loads a contact by destination hash.
func (s *Store) GetContact(ctx context.Context, h identity.Hash) (Contact, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT ed_pub, x_pub, alias, fingerprint, verified, added_at
		FROM contacts WHERE destination_hash = ?
	`, h.String())
	var c Contact
	c.Hash = h
	var edPub, xPub []byte
	var added int64
	var verified int
	if err := row.Scan(&edPub, &xPub, &c.Alias, &c.Fingerprint, &verified, &added); err != nil {
		return Contact{}, err
	}
	c.Public.EdPub = edPub
	copy(c.Public.XPub[:], xPub)
	c.Verified = verified == 1
	c.AddedAt = time.Unix(added, 0).UTC()
	return c, nil
}

// ListContacts returns every contact, ordered by alias then hash.
func (s *Store) ListContacts(ctx context.Context) ([]Contact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT destination_hash, ed_pub, x_pub, alias, fingerprint, verified, added_at
		FROM contacts ORDER BY alias, destination_hash
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		var hashStr string
		var edPub, xPub []byte
		var added int64
		var verified int
		if err := rows.Scan(&hashStr, &edPub, &xPub, &c.Alias, &c.Fingerprint, &verified, &added); err != nil {
			return nil, err
		}
		h, err := identity.ParseHash(hashStr)
		if err != nil {
			continue
		}
		c.Hash = h
		c.Public.EdPub = edPub
		copy(c.Public.XPub[:], xPub)
		c.Verified = verified == 1
		c.AddedAt = time.Unix(added, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetContactVerified flips the verified flag (after out-of-band fingerprint check).
func (s *Store) SetContactVerified(ctx context.Context, h identity.Hash, verified bool) error {
	_, err := s.db.ExecContext(ctx, "UPDATE contacts SET verified = ? WHERE destination_hash = ?", boolInt(verified), h.String())
	return err
}

// MessageKind mirrors chat.MessageKind to avoid an import cycle. Defined
// here as raw int8 so the schema can store it as integer.
type MessageKind int

// HistoryEntry is a row in the messages table.
type HistoryEntry struct {
	ID        int64
	Peer      identity.Hash
	Direction string // "in" / "out"
	Kind      MessageKind
	Body      []byte
	Status    int
	When      time.Time
}

// AppendMessage stores a chat message.
func (s *Store) AppendMessage(ctx context.Context, e HistoryEntry) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (peer_hash, direction, kind, body, status, ts)
		VALUES (?, ?, ?, ?, ?, ?)
	`, e.Peer.String(), e.Direction, int(e.Kind), e.Body, e.Status, e.When.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LoadHistory returns the most recent `limit` messages with peer, oldest first.
func (s *Store) LoadHistory(ctx context.Context, peer identity.Hash, limit int) ([]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, peer_hash, direction, kind, body, status, ts
		FROM messages WHERE peer_hash = ?
		ORDER BY ts DESC LIMIT ?
	`, peer.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var hashStr string
		var ts int64
		var kind int
		if err := rows.Scan(&e.ID, &hashStr, &e.Direction, &kind, &e.Body, &e.Status, &ts); err != nil {
			return nil, err
		}
		h, err := identity.ParseHash(hashStr)
		if err != nil {
			continue
		}
		e.Peer = h
		e.Kind = MessageKind(kind)
		e.When = time.Unix(0, ts).UTC()
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse for oldest-first.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

// MarkMessageStatus updates the delivery status field.
func (s *Store) MarkMessageStatus(ctx context.Context, id int64, status int) error {
	_, err := s.db.ExecContext(ctx, "UPDATE messages SET status = ? WHERE id = ?", status, id)
	return err
}

// OutboxItem is a queued message waiting for the peer to come back online.
type OutboxItem struct {
	ID          int64
	Peer        identity.Hash
	Payload     []byte
	Attempts    int
	LastAttempt time.Time
	CreatedAt   time.Time
}

// AddOutboxItem queues a payload for `peer`.
func (s *Store) AddOutboxItem(ctx context.Context, peer identity.Hash, payload []byte) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO outbox (peer_hash, payload, created_at) VALUES (?, ?, ?)
	`, peer.String(), payload, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// PendingForPeer lists outbox items for a single peer in FIFO order.
func (s *Store) PendingForPeer(ctx context.Context, peer identity.Hash) ([]OutboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, peer_hash, payload, attempts, last_attempt, created_at
		FROM outbox WHERE peer_hash = ? ORDER BY id ASC
	`, peer.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []OutboxItem
	for rows.Next() {
		var it OutboxItem
		var hashStr string
		var lastAttempt, created int64
		if err := rows.Scan(&it.ID, &hashStr, &it.Payload, &it.Attempts, &lastAttempt, &created); err != nil {
			return nil, err
		}
		h, err := identity.ParseHash(hashStr)
		if err != nil {
			continue
		}
		it.Peer = h
		it.LastAttempt = time.Unix(lastAttempt, 0).UTC()
		it.CreatedAt = time.Unix(created, 0).UTC()
		items = append(items, it)
	}
	return items, rows.Err()
}

// AllOutboxPeers returns the list of peers that have queued outbox items.
func (s *Store) AllOutboxPeers(ctx context.Context) ([]identity.Hash, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT peer_hash FROM outbox")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.Hash
	for rows.Next() {
		var hashStr string
		if err := rows.Scan(&hashStr); err != nil {
			return nil, err
		}
		h, err := identity.ParseHash(hashStr)
		if err != nil {
			continue
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// DeleteOutboxItem removes an item once it has been successfully sent.
func (s *Store) DeleteOutboxItem(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", id)
	return err
}

// IncrementOutboxAttempts bumps the attempt counter and records the time.
func (s *Store) IncrementOutboxAttempts(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE outbox SET attempts = attempts + 1, last_attempt = ? WHERE id = ?
	`, time.Now().Unix(), id)
	return err
}

// RecordSeenPeer remembers that we successfully reached `address`. Used by
// the bootstrap layer to seed itself on subsequent runs without a CLI
// --bootstrap flag (design.md §7). Timestamp is stored as unix nanos so
// adjacent inserts within the same second can still be ordered.
func (s *Store) RecordSeenPeer(ctx context.Context, address string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO seen_peers (address, last_seen, success_count) VALUES (?, ?, 1)
		ON CONFLICT(address) DO UPDATE SET
			last_seen = excluded.last_seen,
			success_count = success_count + 1
	`, address, time.Now().UnixNano())
	return err
}

// SeenPeers returns up to `limit` cached bootstrap addresses, most recent
// first. limit<=0 returns all.
func (s *Store) SeenPeers(ctx context.Context, limit int) ([]string, error) {
	q := "SELECT address FROM seen_peers ORDER BY last_seen DESC"
	args := []any{}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		out = append(out, addr)
	}
	return out, rows.Err()
}

// ForgetSeenPeer removes a cached bootstrap entry — used after repeated
// dial failures so we stop wasting startup time on a dead address.
func (s *Store) ForgetSeenPeer(ctx context.Context, address string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM seen_peers WHERE address = ?", address)
	return err
}

// SeenPeersDiverse returns up to `limit` cached bootstrap addresses,
// preferring diverse /24 IPv4 (and full IPv6) prefixes so the next
// startup is harder to eclipse with a Sybil cluster colocated in one
// subnet (design.md §8). Within each prefix the most-recent address
// wins; prefixes are ordered by their freshest entry's last_seen.
func (s *Store) SeenPeersDiverse(ctx context.Context, limit int) ([]string, error) {
	all, err := s.SeenPeers(ctx, 0)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, limit)
	for _, addr := range all {
		key := subnetKey(addr)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, addr)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// subnetKey reduces an "ip:port" / "host:port" address to the prefix the
// diverse-bootstrap filter should treat as a single neighbourhood. For
// IPv4 — first three octets ("/24") rendered as "10.0.0". For IPv6 —
// first 64 bits ("/64") rendered as "2001:db8::/64"-style hex. For
// non-IP / unparsable addresses the host field itself is used.
//
// The returned string is only used as a map key, so any deterministic
// representation works — but a readable one is friendlier when these
// land in debug logs.
func subnetKey(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d", v4[0], v4[1], v4[2])
	}
	v6 := ip.To16()

	return fmt.Sprintf("%x:%x:%x:%x", v6[0:2], v6[2:4], v6[4:6], v6[6:8])
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

