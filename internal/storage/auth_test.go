package storage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/storage"
)

func TestAuthCredentials_PassphraseUpsert(t *testing.T) {
	t.Parallel()
	s := mkStore(t)

	got, err := s.GetAuthCredentials(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil before any set; got %+v", got)
	}

	if err := s.SetPassphrase(t.Context(), "encoded-hash-v1"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAuthCredentials(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.PassphraseHash != "encoded-hash-v1" {
		t.Fatalf("after first set: got %+v", got)
	}
	if got.TOTPSecret != nil {
		t.Errorf("totp should be nil until enrolled; got %x", got.TOTPSecret)
	}

	if err := s.SetPassphrase(t.Context(), "encoded-hash-v2"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAuthCredentials(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.PassphraseHash != "encoded-hash-v2" {
		t.Errorf("update did not stick: %s", got.PassphraseHash)
	}
}

func TestAuthCredentials_Delete(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	ctx := t.Context()

	// Idempotent before any set.
	if err := s.DeleteAuthCredentials(ctx); err != nil {
		t.Fatalf("DeleteAuthCredentials before set: %v", err)
	}

	if err := s.SetPassphrase(ctx, "encoded-hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTOTPSecret(ctx, []byte("0123456789abcdef0123")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAuthCredentials(ctx); got == nil {
		t.Fatal("setup: creds must be present before delete")
	}

	// Live session before reset — must be invalidated by DeleteAuthCredentials,
	// otherwise a previously-stolen cookie keeps full API access until idle TTL.
	now := time.Now()
	if err := s.CreateAuthSession(ctx, storage.AuthSession{
		ID: "sess-pre-reset", CreatedAt: now, LastSeen: now,
		RemoteIP: "1.2.3.4", UserAgent: "x",
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteAuthCredentials(ctx); err != nil {
		t.Fatalf("DeleteAuthCredentials: %v", err)
	}
	got, err := s.GetAuthCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil after delete, got %+v", got)
	}

	// Session row must be wiped — without this fix, Validate() still finds it
	// and the operator's "reset" becomes a security non-event.
	sess, err := s.GetAuthSession(ctx, "sess-pre-reset")
	if err != nil {
		t.Fatal(err)
	}
	if sess != nil {
		t.Errorf("DeleteAuthCredentials must wipe live sessions; got %+v", sess)
	}

	// SetTOTPSecret on the now-cleared row must surface ErrCredentialsNotSet
	// (matches the "no passphrase yet" semantics — the row really is gone).
	if err := s.SetTOTPSecret(ctx, []byte("0123456789abcdef0123")); !errors.Is(err, storage.ErrCredentialsNotSet) {
		t.Errorf("SetTOTPSecret after delete: want ErrCredentialsNotSet, got %v", err)
	}
}

func TestAuthCredentials_TOTPLifecycle(t *testing.T) {
	t.Parallel()
	s := mkStore(t)

	if err := s.SetTOTPSecret(t.Context(), []byte("0123456789abcdef0123")); !errors.Is(err, storage.ErrCredentialsNotSet) {
		t.Errorf("setting TOTP without passphrase should fail with ErrCredentialsNotSet; got %v", err)
	}

	if err := s.SetPassphrase(t.Context(), "phash"); err != nil {
		t.Fatal(err)
	}
	secret := []byte("0123456789abcdef0123")
	if err := s.SetTOTPSecret(t.Context(), secret); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAuthCredentials(t.Context())
	if string(got.TOTPSecret) != string(secret) {
		t.Errorf("totp secret mismatch")
	}

	if err := s.ClearTOTPSecret(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAuthCredentials(t.Context())
	if got.TOTPSecret != nil {
		t.Errorf("totp secret should be nil after clear; got %x", got.TOTPSecret)
	}
}

func TestRecoveryCodes_ResetAndConsume(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	if err := s.SetPassphrase(t.Context(), "phash"); err != nil {
		t.Fatal(err)
	}

	if err := s.ResetRecoveryCodes(t.Context(), []string{"h1", "h2", "h3"}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.UnconsumedRecoveryCodes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 unconsumed; got %d", len(rows))
	}

	consumeAt := time.Unix(1700000000, 0).UTC()
	ok, err := s.MarkRecoveryCodeConsumed(t.Context(), rows[1].ID, consumeAt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("first MarkRecoveryCodeConsumed reported no row consumed")
	}
	dup, err := s.MarkRecoveryCodeConsumed(t.Context(), rows[1].ID, consumeAt)
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Errorf("second MarkRecoveryCodeConsumed on the same row returned true; race window not closed")
	}
	rows, _ = s.UnconsumedRecoveryCodes(t.Context())
	if len(rows) != 2 {
		t.Fatalf("expected 2 unconsumed after consume; got %d", len(rows))
	}
	for _, r := range rows {
		if r.Hash == "h2" {
			t.Errorf("consumed hash still present: %s", r.Hash)
		}
	}

	// Reset wipes everything (consumed too) and inserts the new set.
	if err := s.ResetRecoveryCodes(t.Context(), []string{"new1", "new2"}); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.UnconsumedRecoveryCodes(t.Context())
	if len(rows) != 2 {
		t.Fatalf("expected 2 after reset; got %d", len(rows))
	}
}

func TestAuthSession_CRUD(t *testing.T) {
	t.Parallel()
	s := mkStore(t)

	now := time.Unix(1700000000, 0).UTC()
	sess := storage.AuthSession{
		ID:        "session-id-abc",
		CreatedAt: now,
		LastSeen:  now,
		RemoteIP:  "203.0.113.10",
		UserAgent: "curl/8",
	}
	if err := s.CreateAuthSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetAuthSession(t.Context(), "session-id-abc")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.RemoteIP != "203.0.113.10" {
		t.Fatalf("get: %+v", got)
	}

	later := now.Add(5 * time.Minute)
	if err := s.TouchAuthSession(t.Context(), "session-id-abc", later); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAuthSession(t.Context(), "session-id-abc")
	if !got.LastSeen.Equal(later) {
		t.Errorf("last_seen not updated; got %s want %s", got.LastSeen, later)
	}

	if err := s.DeleteAuthSession(t.Context(), "session-id-abc"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAuthSession(t.Context(), "session-id-abc")
	if got != nil {
		t.Errorf("session still present after delete")
	}

	got, err = s.GetAuthSession(t.Context(), "nonexistent")
	if err != nil {
		t.Errorf("missing session should be (nil, nil); got err=%v", err)
	}
	if got != nil {
		t.Errorf("missing session should be nil; got %+v", got)
	}
}

func TestAuthSession_Prune(t *testing.T) {
	t.Parallel()
	s := mkStore(t)

	now := time.Unix(1700000000, 0).UTC()
	sessions := []storage.AuthSession{
		{ID: "fresh", CreatedAt: now, LastSeen: now, RemoteIP: "1.1.1.1"},
		{ID: "stale", CreatedAt: now.Add(-30 * 24 * time.Hour), LastSeen: now.Add(-10 * 24 * time.Hour), RemoteIP: "2.2.2.2"},
	}
	for _, sess := range sessions {
		if err := s.CreateAuthSession(t.Context(), sess); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := s.PruneAuthSessions(t.Context(), 7*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	got, _ := s.GetAuthSession(t.Context(), "fresh")
	if got == nil {
		t.Errorf("fresh session pruned by mistake")
	}
	got, _ = s.GetAuthSession(t.Context(), "stale")
	if got != nil {
		t.Errorf("stale session not pruned")
	}
}

func TestAuthLog_AppendTailPrune(t *testing.T) {
	t.Parallel()
	s := mkStore(t)

	base := time.Unix(1700000000, 0).UTC()
	for i := range 12 {
		if err := s.WriteAuthLog(t.Context(), storage.AuthLogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Event:     "login_attempt",
			RemoteIP:  "10.0.0.1",
			Note:      "n",
		}); err != nil {
			t.Fatal(err)
		}
	}

	tail, err := s.AuthLogTail(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 5 {
		t.Fatalf("tail len = %d, want 5", len(tail))
	}
	// Most-recent first.
	if !tail[0].Timestamp.After(tail[4].Timestamp) {
		t.Errorf("tail not ordered most-recent first: %v ... %v", tail[0].Timestamp, tail[4].Timestamp)
	}

	pruned, err := s.PruneAuthLog(t.Context(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 4 {
		t.Errorf("pruned = %d, want 4 (12-8)", pruned)
	}
	tail, _ = s.AuthLogTail(t.Context(), 100)
	if len(tail) != 8 {
		t.Errorf("after prune len = %d, want 8", len(tail))
	}
}
