package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrCredentialsNotSet is returned by methods that require an existing
// auth_credentials row (e.g. enabling TOTP) before any passphrase has been set.
var ErrCredentialsNotSet = errors.New("storage: auth credentials not set")

// AuthCredentials is the persisted single-row view of webui auth state.
type AuthCredentials struct {
	PassphraseHash string
	TOTPSecret     []byte
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// GetAuthCredentials returns the single credentials row, or (nil, nil) if
// no row has been written yet (i.e. -set-password has not been run).
func (s *Store) GetAuthCredentials(ctx context.Context) (*AuthCredentials, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT passphrase_hash, totp_secret, created_at, updated_at
		 FROM auth_credentials WHERE id = 1`)
	var (
		ph        string
		secret    []byte
		createdAt int64
		updatedAt int64
	)
	if err := row.Scan(&ph, &secret, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, fmt.Errorf("storage: scan auth_credentials: %w", err)
	}

	return &AuthCredentials{
		PassphraseHash: ph,
		TOTPSecret:     secret,
		CreatedAt:      time.Unix(createdAt, 0).UTC(),
		UpdatedAt:      time.Unix(updatedAt, 0).UTC(),
	}, nil
}

// SetPassphrase upserts the single credentials row, leaving TOTP secret
// untouched on update so that rotating a passphrase does not silently
// disenroll the second factor.
func (s *Store) SetPassphrase(ctx context.Context, encodedHash string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_credentials (id, passphrase_hash, totp_secret, created_at, updated_at)
		VALUES (1, ?, NULL, ?, ?)
		ON CONFLICT(id) DO UPDATE SET passphrase_hash = excluded.passphrase_hash, updated_at = excluded.updated_at
	`, encodedHash, now, now)
	if err != nil {
		return fmt.Errorf("storage: set passphrase: %w", err)
	}

	return nil
}

// SetTOTPSecret stores the raw TOTP secret. Returns ErrCredentialsNotSet
// if no passphrase row exists — TOTP without a passphrase makes no sense.
func (s *Store) SetTOTPSecret(ctx context.Context, secret []byte) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE auth_credentials SET totp_secret = ?, updated_at = ? WHERE id = 1
	`, secret, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("storage: set totp: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrCredentialsNotSet
	}

	return nil
}

// ClearTOTPSecret disenrolls TOTP. Idempotent — no error if there was no
// secret to begin with.
func (s *Store) ClearTOTPSecret(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE auth_credentials SET totp_secret = NULL, updated_at = ? WHERE id = 1
	`, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("storage: clear totp: %w", err)
	}

	return nil
}

// RecoveryCodeRow is one stored hash, exposed so the auth handler can
// iterate and Argon2id-verify each one against a user-entered code.
type RecoveryCodeRow struct {
	ID   int64
	Hash string
}

// ResetRecoveryCodes wipes any prior codes (consumed and unconsumed) and
// inserts the given hashes atomically. Used when a user re-enrolls TOTP.
func (s *Store) ResetRecoveryCodes(ctx context.Context, hashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_recovery_codes`); err != nil {
		return fmt.Errorf("storage: wipe recovery codes: %w", err)
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_recovery_codes (hash) VALUES (?)`, h); err != nil {
			return fmt.Errorf("storage: insert recovery code: %w", err)
		}
	}

	return tx.Commit()
}

// UnconsumedRecoveryCodes returns rows where consumed_at is still 0.
func (s *Store) UnconsumedRecoveryCodes(ctx context.Context) ([]RecoveryCodeRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, hash FROM auth_recovery_codes WHERE consumed_at = 0 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("storage: query recovery codes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RecoveryCodeRow
	for rows.Next() {
		var r RecoveryCodeRow
		if err := rows.Scan(&r.ID, &r.Hash); err != nil {
			return nil, fmt.Errorf("storage: scan recovery code: %w", err)
		}
		out = append(out, r)
	}

	return out, rows.Err()
}

// MarkRecoveryCodeConsumed atomically stamps consumed_at on a row that
// was previously unconsumed. Returns true iff exactly one row transitioned;
// false means the code was already consumed by a concurrent request. The
// caller MUST treat false as a verification failure to defeat TOCTOU
// races where two requests with the same code race past tryRecoveryCode's
// hash-verify step.
func (s *Store) MarkRecoveryCodeConsumed(ctx context.Context, id int64, when time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE auth_recovery_codes SET consumed_at = ? WHERE id = ? AND consumed_at = 0`,
		when.Unix(), id)
	if err != nil {
		return false, fmt.Errorf("storage: consume recovery code: %w", err)
	}
	n, _ := res.RowsAffected()

	return n == 1, nil
}

// AuthSession is the persisted view of a logged-in browser session.
type AuthSession struct {
	ID        string
	CreatedAt time.Time
	LastSeen  time.Time
	RemoteIP  string
	UserAgent string
}

// CreateAuthSession inserts a new session row. The caller has already
// authenticated; this just persists the cookie-id binding.
func (s *Store) CreateAuthSession(ctx context.Context, sess AuthSession) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_sessions (id, created_at, last_seen, remote_ip, user_agent)
		VALUES (?, ?, ?, ?, ?)
	`, sess.ID, sess.CreatedAt.Unix(), sess.LastSeen.Unix(), sess.RemoteIP, sess.UserAgent)
	if err != nil {
		return fmt.Errorf("storage: create session: %w", err)
	}

	return nil
}

// GetAuthSession returns (nil, nil) if no row matches id.
func (s *Store) GetAuthSession(ctx context.Context, id string) (*AuthSession, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, created_at, last_seen, remote_ip, user_agent
		FROM auth_sessions WHERE id = ?`, id)
	var (
		out      AuthSession
		created  int64
		lastSeen int64
	)
	if err := row.Scan(&out.ID, &created, &lastSeen, &out.RemoteIP, &out.UserAgent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, fmt.Errorf("storage: scan session: %w", err)
	}
	out.CreatedAt = time.Unix(created, 0).UTC()
	out.LastSeen = time.Unix(lastSeen, 0).UTC()

	return &out, nil
}

// TouchAuthSession updates last_seen. Cheap; called on every request when
// the on-disk last_seen is stale beyond the rolling-update threshold.
func (s *Store) TouchAuthSession(ctx context.Context, id string, when time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE auth_sessions SET last_seen = ? WHERE id = ?`, when.Unix(), id)
	if err != nil {
		return fmt.Errorf("storage: touch session: %w", err)
	}

	return nil
}

// DeleteAuthSession removes a session row. Used on logout.
func (s *Store) DeleteAuthSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete session: %w", err)
	}

	return nil
}

// DeleteAuthSessionsExcept removes every session row but the caller's
// current id. Used after passphrase rotation to bounce other devices.
// An empty keepID wipes every row (useful from CLI when the operator
// has lost access).
func (s *Store) DeleteAuthSessionsExcept(ctx context.Context, keepID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE id != ?`, keepID)
	if err != nil {
		return fmt.Errorf("storage: delete other sessions: %w", err)
	}

	return nil
}

// ListAuthSessions returns every persisted session ordered by most-recent
// activity first. Used by the Settings → Security panel to render the
// "active devices" list.
func (s *Store) ListAuthSessions(ctx context.Context) ([]AuthSession, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at, last_seen, remote_ip, user_agent
		FROM auth_sessions ORDER BY last_seen DESC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AuthSession
	for rows.Next() {
		var (
			s        AuthSession
			created  int64
			lastSeen int64
		)
		if err := rows.Scan(&s.ID, &created, &lastSeen, &s.RemoteIP, &s.UserAgent); err != nil {
			return nil, fmt.Errorf("storage: scan session: %w", err)
		}
		s.CreatedAt = time.Unix(created, 0).UTC()
		s.LastSeen = time.Unix(lastSeen, 0).UTC()
		out = append(out, s)
	}

	return out, rows.Err()
}

// CountUnconsumedRecoveryCodes returns the number of recovery codes still
// available — surfaces "N codes left" in the security UI without leaking
// the codes themselves.
func (s *Store) CountUnconsumedRecoveryCodes(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM auth_recovery_codes WHERE consumed_at = 0`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("storage: count recovery codes: %w", err)
	}

	return n, nil
}

// PruneAuthSessions removes sessions whose last_seen is older than
// (now - maxIdle). Returns the number of rows deleted.
func (s *Store) PruneAuthSessions(ctx context.Context, maxIdle time.Duration, now time.Time) (int, error) {
	cutoff := now.Add(-maxIdle).Unix()
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE last_seen < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("storage: prune sessions: %w", err)
	}
	n, _ := res.RowsAffected()

	return int(n), nil
}

// AuthLogEntry is one audit row.
type AuthLogEntry struct {
	Timestamp time.Time
	Event     string
	RemoteIP  string
	UserAgent string
	Note      string
}

// WriteAuthLog appends a row.
func (s *Store) WriteAuthLog(ctx context.Context, e AuthLogEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_log (ts, event, remote_ip, user_agent, note)
		VALUES (?, ?, ?, ?, ?)
	`, e.Timestamp.Unix(), e.Event, e.RemoteIP, e.UserAgent, e.Note)
	if err != nil {
		return fmt.Errorf("storage: write auth_log: %w", err)
	}

	return nil
}

// AuthLogTail returns the most-recent rows first, capped at limit.
func (s *Store) AuthLogTail(ctx context.Context, limit int) ([]AuthLogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, event, remote_ip, user_agent, note
		FROM auth_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: query auth_log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]AuthLogEntry, 0, limit)
	for rows.Next() {
		var (
			e  AuthLogEntry
			ts int64
		)
		if err := rows.Scan(&ts, &e.Event, &e.RemoteIP, &e.UserAgent, &e.Note); err != nil {
			return nil, fmt.Errorf("storage: scan auth_log: %w", err)
		}
		e.Timestamp = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}

	return out, rows.Err()
}

// PruneAuthLog keeps only the last `keep` rows by id (effectively by
// insertion order). Returns the number of rows deleted.
func (s *Store) PruneAuthLog(ctx context.Context, keep int) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM auth_log
		WHERE id NOT IN (SELECT id FROM auth_log ORDER BY id DESC LIMIT ?)
	`, keep)
	if err != nil {
		return 0, fmt.Errorf("storage: prune auth_log: %w", err)
	}
	n, _ := res.RowsAffected()

	return int(n), nil
}
