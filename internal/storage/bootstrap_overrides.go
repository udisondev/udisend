package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrBootstrapOverrideNotFound is returned when an address is not present
// in the bootstrap_overrides table.
var ErrBootstrapOverrideNotFound = errors.New("storage: bootstrap override not found")

// BootstrapOverride is one user-managed bootstrap entry.
type BootstrapOverride struct {
	Address      string
	Enabled      bool
	Note         string
	AddedAt      time.Time
	LastStatus   string // "", "ok", "fail"
	LastStatusAt time.Time
}

// AddBootstrapOverride inserts a new entry. Idempotent on address — a
// repeated call updates note/enabled but never resets last_status.
func (s *Store) AddBootstrapOverride(ctx context.Context, addr, note string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO bootstrap_overrides (address, enabled, note, added_at)
		VALUES (?, 1, ?, ?)
		ON CONFLICT(address) DO UPDATE SET
			enabled = 1,
			note = excluded.note
	`, addr, note, now)
	if err != nil {
		return fmt.Errorf("storage: add bootstrap override: %w", err)
	}

	return nil
}

// RemoveBootstrapOverride deletes the row by address. Returns
// ErrBootstrapOverrideNotFound if no row matched.
func (s *Store) RemoveBootstrapOverride(ctx context.Context, addr string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM bootstrap_overrides WHERE address = ?`, addr)
	if err != nil {
		return fmt.Errorf("storage: remove bootstrap override: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrBootstrapOverrideNotFound
	}

	return nil
}

// SetBootstrapOverrideEnabled flips the enabled flag. Returns
// ErrBootstrapOverrideNotFound if no row matched.
func (s *Store) SetBootstrapOverrideEnabled(ctx context.Context, addr string, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE bootstrap_overrides SET enabled = ? WHERE address = ?`,
		boolInt(enabled), addr)
	if err != nil {
		return fmt.Errorf("storage: toggle bootstrap override: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrBootstrapOverrideNotFound
	}

	return nil
}

// MarkBootstrapStatus records the result of the last bootstrap attempt
// against an address. Status is "ok" on success, "fail" otherwise. The
// row is created on first success even if not previously present (covers
// the case of the cache-promotion path); for explicit user-managed
// entries it is upserted as-disabled if missing — but in practice the
// caller funnels every status update through this method, so it must
// only touch existing rows. Missing rows are silently ignored: a status
// for an address the user has since removed must not resurrect the row.
func (s *Store) MarkBootstrapStatus(ctx context.Context, addr, status string, when time.Time) error {
	switch status {
	case "ok", "fail", "":
	default:
		return fmt.Errorf("storage: invalid bootstrap status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE bootstrap_overrides
		   SET last_status = ?, last_status_at = ?
		 WHERE address = ?
	`, status, when.Unix(), addr)
	if err != nil {
		return fmt.Errorf("storage: mark bootstrap status: %w", err)
	}

	return nil
}

// ListBootstrapOverrides returns every row, newest first.
func (s *Store) ListBootstrapOverrides(ctx context.Context) ([]BootstrapOverride, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT address, enabled, note, added_at, last_status, last_status_at
		  FROM bootstrap_overrides
		 ORDER BY added_at DESC, address ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list bootstrap overrides: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []BootstrapOverride
	for rows.Next() {
		var (
			b            BootstrapOverride
			enabled      int
			added        int64
			lastStatusAt int64
		)
		if err := rows.Scan(&b.Address, &enabled, &b.Note, &added, &b.LastStatus, &lastStatusAt); err != nil {
			return nil, fmt.Errorf("storage: scan bootstrap override: %w", err)
		}
		b.Enabled = enabled != 0
		b.AddedAt = time.Unix(added, 0).UTC()
		if lastStatusAt > 0 {
			b.LastStatusAt = time.Unix(lastStatusAt, 0).UTC()
		}
		out = append(out, b)
	}

	return out, rows.Err()
}

// EnabledBootstrapOverrides returns just the addresses that should be
// fed into the bootstrap loop. Order: newest-added first.
func (s *Store) EnabledBootstrapOverrides(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT address FROM bootstrap_overrides
		 WHERE enabled = 1
		 ORDER BY added_at DESC, address ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: enabled bootstrap overrides: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("storage: scan bootstrap override addr: %w", err)
		}
		out = append(out, a)
	}

	return out, rows.Err()
}

// BootstrapOverrideExists is a thin convenience for handlers that need
// to differentiate "address not in user list" from "address present and
// enabled" without round-tripping the full row.
func (s *Store) BootstrapOverrideExists(ctx context.Context, addr string) (bool, error) {
	var dummy int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM bootstrap_overrides WHERE address = ?`, addr).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: lookup bootstrap override: %w", err)
	}

	return true, nil
}
