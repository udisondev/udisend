package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetSetting returns the value for key. The second return is false if no
// row exists yet — distinct from an empty string, which is a valid value.
func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("storage: get setting %q: %w", key, err)
	}

	return v, true, nil
}

// SetSetting upserts a key/value pair.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	if err != nil {
		return fmt.Errorf("storage: set setting %q: %w", key, err)
	}

	return nil
}
