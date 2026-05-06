package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrICEOverrideNotFound — addr-not-in-table sentinel.
var ErrICEOverrideNotFound = errors.New("storage: ice override not found")

// ICEOverride is one user-curated STUN/TURN server.
type ICEOverride struct {
	URL        string
	Username   string
	Credential string
	Enabled    bool
	AddedAt    time.Time
}

// AddICEOverride upserts by URL. Idempotent: a repeated call refreshes
// credentials and re-enables a previously-disabled row.
func (s *Store) AddICEOverride(ctx context.Context, e ICEOverride) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ice_overrides (url, username, credential, enabled, added_at)
		VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(url) DO UPDATE SET
			username = excluded.username,
			credential = excluded.credential,
			enabled = 1
	`, e.URL, e.Username, e.Credential, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("storage: add ice override: %w", err)
	}

	return nil
}

// RemoveICEOverride deletes by URL.
func (s *Store) RemoveICEOverride(ctx context.Context, url string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ice_overrides WHERE url = ?`, url)
	if err != nil {
		return fmt.Errorf("storage: remove ice override: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrICEOverrideNotFound
	}

	return nil
}

// SetICEOverrideEnabled flips the enabled flag.
func (s *Store) SetICEOverrideEnabled(ctx context.Context, url string, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE ice_overrides SET enabled = ? WHERE url = ?`,
		boolInt(enabled), url)
	if err != nil {
		return fmt.Errorf("storage: toggle ice override: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrICEOverrideNotFound
	}

	return nil
}

// ListICEOverrides returns every row, newest first.
func (s *Store) ListICEOverrides(ctx context.Context) ([]ICEOverride, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT url, username, credential, enabled, added_at
		  FROM ice_overrides
		 ORDER BY added_at DESC, url ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list ice overrides: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ICEOverride
	for rows.Next() {
		var (
			e       ICEOverride
			enabled int
			added   int64
		)
		if err := rows.Scan(&e.URL, &e.Username, &e.Credential, &enabled, &added); err != nil {
			return nil, fmt.Errorf("storage: scan ice override: %w", err)
		}
		e.Enabled = enabled != 0
		e.AddedAt = time.Unix(added, 0).UTC()
		out = append(out, e)
	}

	return out, rows.Err()
}

// EnabledICEOverrides returns only the rows with enabled=1.
func (s *Store) EnabledICEOverrides(ctx context.Context) ([]ICEOverride, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT url, username, credential, enabled, added_at
		  FROM ice_overrides WHERE enabled = 1
		 ORDER BY added_at DESC, url ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: enabled ice overrides: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ICEOverride
	for rows.Next() {
		var (
			e       ICEOverride
			enabled int
			added   int64
		)
		if err := rows.Scan(&e.URL, &e.Username, &e.Credential, &enabled, &added); err != nil {
			return nil, fmt.Errorf("storage: scan ice override: %w", err)
		}
		e.Enabled = enabled != 0
		e.AddedAt = time.Unix(added, 0).UTC()
		out = append(out, e)
	}

	return out, rows.Err()
}

// MessageBytes is a debug-only helper used by Settings → Privacy → Storage
// to surface roughly how much the message log is contributing to the DB
// size. Counts via SUM(LENGTH(body)) on the messages table.
func (s *Store) MessageBytes(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(LENGTH(body)), 0) FROM messages`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("storage: sum message bytes: %w", err)
	}

	return n.Int64, nil
}

// CountMessages counts every row in messages.
func (s *Store) CountMessages(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("storage: count messages: %w", err)
	}

	return n, nil
}

// CountContacts counts every row in contacts.
func (s *Store) CountContacts(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("storage: count contacts: %w", err)
	}

	return n, nil
}

// CountOutbox counts the queued outbox items (across all peers).
func (s *Store) CountOutbox(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("storage: count outbox: %w", err)
	}

	return n, nil
}

// Vacuum runs VACUUM and returns the number of bytes reclaimed (db file
// size before − after). The caller passes the absolute path to the
// underlying database file so we can stat it; SQLite has no programmatic
// "current db file" accessor on stock connections.
func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	if err != nil {
		return fmt.Errorf("storage: vacuum: %w", err)
	}

	return nil
}

// PruneMessagesOlderThan deletes message rows whose timestamp is older
// than cutoff (unix nanos comparison). Returns rows deleted. Used by the
// auto-delete-history setting in messenger Run loop.
func (s *Store) PruneMessagesOlderThan(ctx context.Context, cutoffUnixNanos int64) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE ts < ?`, cutoffUnixNanos)
	if err != nil {
		return 0, fmt.Errorf("storage: prune old messages: %w", err)
	}
	n, _ := res.RowsAffected()

	return int(n), nil
}
