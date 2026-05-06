package auth

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/storage"
)

func mkAuthStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return store
}

func TestSessions_BeginValidate(t *testing.T) {
	t.Parallel()
	store := mkAuthStore(t)
	s := NewSessions(store, SessionsConfig{Idle: 24 * time.Hour, RollingTouch: 5 * time.Minute})

	id, err := s.Begin(t.Context(), "203.0.113.10", "curl/8")
	if err != nil {
		t.Fatal(err)
	}
	if len(id) < 32 {
		t.Errorf("session id too short: %d chars", len(id))
	}

	sess, err := s.Validate(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess == nil || sess.ID != id {
		t.Errorf("validate returned %+v", sess)
	}
}

func TestSessions_Validate_UnknownIDReturnsNil(t *testing.T) {
	t.Parallel()
	store := mkAuthStore(t)
	s := NewSessions(store, SessionsConfig{Idle: 24 * time.Hour, RollingTouch: 5 * time.Minute})

	got, err := s.Validate(t.Context(), "nope")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("validate(unknown) = %+v, want nil", got)
	}
}

func TestSessions_Validate_IdleExpiresAndCleansUp(t *testing.T) {
	t.Parallel()
	store := mkAuthStore(t)

	now := time.Unix(1700000000, 0).UTC()
	s := NewSessions(store, SessionsConfig{
		Idle:         time.Hour,
		RollingTouch: 5 * time.Minute,
		Now:          func() time.Time { return now },
	})
	id, err := s.Begin(t.Context(), "1.1.1.1", "ua")
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Hour) // beyond Idle
	sess, err := s.Validate(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if sess != nil {
		t.Errorf("expired session returned: %+v", sess)
	}

	// Validate must have evicted the row.
	row, _ := store.GetAuthSession(t.Context(), id)
	if row != nil {
		t.Errorf("expired session not cleaned up; still in store")
	}
}

func TestSessions_Validate_RollingTouch(t *testing.T) {
	t.Parallel()
	store := mkAuthStore(t)

	now := time.Unix(1700000000, 0).UTC()
	s := NewSessions(store, SessionsConfig{
		Idle:         24 * time.Hour,
		RollingTouch: 5 * time.Minute,
		Now:          func() time.Time { return now },
	})
	id, _ := s.Begin(t.Context(), "1.1.1.1", "ua")
	original, _ := store.GetAuthSession(t.Context(), id)

	now = now.Add(time.Minute) // less than RollingTouch
	if _, err := s.Validate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	row, _ := store.GetAuthSession(t.Context(), id)
	if !row.LastSeen.Equal(original.LastSeen) {
		t.Errorf("LastSeen updated within rolling threshold; want unchanged")
	}

	now = now.Add(10 * time.Minute) // beyond RollingTouch
	if _, err := s.Validate(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	row, _ = store.GetAuthSession(t.Context(), id)
	if row.LastSeen.Equal(original.LastSeen) {
		t.Errorf("LastSeen not updated after rolling threshold passed")
	}
}

func TestSessions_End(t *testing.T) {
	t.Parallel()
	store := mkAuthStore(t)
	s := NewSessions(store, SessionsConfig{Idle: 24 * time.Hour, RollingTouch: 5 * time.Minute})

	id, _ := s.Begin(t.Context(), "1.1.1.1", "ua")
	if err := s.End(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Validate(t.Context(), id)
	if got != nil {
		t.Errorf("session present after End")
	}
}
