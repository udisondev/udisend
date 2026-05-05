package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/udisondev/udisend/internal/storage"
)

// SessionStore is the persistence surface that Sessions depends on. The
// concrete *storage.Store satisfies it; tests can supply a fake.
type SessionStore interface {
	CreateAuthSession(context.Context, storage.AuthSession) error
	GetAuthSession(context.Context, string) (*storage.AuthSession, error)
	TouchAuthSession(context.Context, string, time.Time) error
	DeleteAuthSession(context.Context, string) error
	PruneAuthSessions(context.Context, time.Duration, time.Time) (int, error)
}

// SessionsConfig parametrises lifecycles. Idle controls when an inactive
// session is evicted; RollingTouch controls how often we re-write
// last_seen (a per-request write would burn IO needlessly).
type SessionsConfig struct {
	Idle         time.Duration
	RollingTouch time.Duration
	Now          func() time.Time
	RandSource   io.Reader
}

// Sessions is the high-level cookie-session manager.
type Sessions struct {
	store SessionStore
	cfg   SessionsConfig
}

// NewSessions constructs a Sessions with sensible test/prod defaults.
func NewSessions(store SessionStore, cfg SessionsConfig) *Sessions {
	if cfg.Idle == 0 {
		cfg.Idle = 30 * 24 * time.Hour
	}
	if cfg.RollingTouch == 0 {
		cfg.RollingTouch = 5 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RandSource == nil {
		cfg.RandSource = rand.Reader
	}

	return &Sessions{store: store, cfg: cfg}
}

// Begin creates a new session and returns its opaque id (the cookie value).
// Caller is responsible for setting the cookie with HttpOnly+Secure+SameSite.
func (s *Sessions) Begin(ctx context.Context, remoteIP, userAgent string) (string, error) {
	now := s.cfg.Now().UTC()
	id, err := newSessionID(s.cfg.RandSource)
	if err != nil {
		return "", err
	}

	sess := storage.AuthSession{
		ID:        id,
		CreatedAt: now,
		LastSeen:  now,
		RemoteIP:  remoteIP,
		UserAgent: userAgent,
	}
	if err := s.store.CreateAuthSession(ctx, sess); err != nil {
		return "", err
	}

	return id, nil
}

// Validate looks up id in the store. Returns (nil, nil) for unknown ids
// and for sessions that have exceeded the Idle window (which are also
// evicted from the store as a side-effect, so a single validate of an
// expired session does the cleanup the periodic prune would have done).
//
// On a hit, Validate may bump last_seen if the rolling-touch threshold has
// passed since the row was last written; this throttles disk writes.
func (s *Sessions) Validate(ctx context.Context, id string) (*storage.AuthSession, error) {
	if id == "" {
		return nil, nil
	}
	sess, err := s.store.GetAuthSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, nil
	}

	now := s.cfg.Now().UTC()
	if now.Sub(sess.LastSeen) > s.cfg.Idle {
		if delErr := s.store.DeleteAuthSession(ctx, id); delErr != nil {
			return nil, delErr
		}

		return nil, nil
	}

	if now.Sub(sess.LastSeen) >= s.cfg.RollingTouch {
		if err := s.store.TouchAuthSession(ctx, id, now); err != nil {
			return nil, err
		}
		sess.LastSeen = now
	}

	return sess, nil
}

// End deletes the session row. Used on explicit logout.
func (s *Sessions) End(ctx context.Context, id string) error {
	return s.store.DeleteAuthSession(ctx, id)
}

// Prune evicts all sessions whose LastSeen is older than Idle. Wire to a
// goroutine on a coarse timer (e.g. once per hour).
func (s *Sessions) Prune(ctx context.Context) (int, error) {
	return s.store.PruneAuthSessions(ctx, s.cfg.Idle, s.cfg.Now().UTC())
}

func newSessionID(r io.Reader) (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("auth: read session id: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
