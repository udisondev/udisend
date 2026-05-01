package storage_test

import (
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/udisondev/udisend/internal/storage"
	"github.com/udisondev/udisend/pkg/identity"
)

func mkStore(t *testing.T) *storage.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := storage.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_ContactsRoundtrip(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id, _ := identity.Generate(rand.Reader)
	c := storage.Contact{
		Hash:        id.Public().DestinationHash(),
		Public:      id.Public(),
		Alias:       "alice",
		Fingerprint: id.Public().Fingerprint(),
		AddedAt:     time.Now().Truncate(time.Second).UTC(),
	}
	if err := s.UpsertContact(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetContact(t.Context(), c.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.Alias != "alice" {
		t.Fatal("alias")
	}
	if got.Public.DestinationHash() != c.Hash {
		t.Fatal("hash mismatch after roundtrip")
	}
}

func TestStore_ContactFingerprintChange(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id1, _ := identity.Generate(rand.Reader)
	id2, _ := identity.Generate(rand.Reader)
	c := storage.Contact{
		Hash:        id1.Public().DestinationHash(),
		Public:      id1.Public(),
		Alias:       "x",
		Fingerprint: id1.Public().Fingerprint(),
		AddedAt:     time.Now(),
	}
	if err := s.UpsertContact(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	c2 := c
	c2.Public = id2.Public() // different keys, same hash slot — simulate impostor
	if err := s.UpsertContact(t.Context(), c2); !errors.Is(err, storage.ErrFingerprintChanged) {
		t.Fatalf("err = %v, want ErrFingerprintChanged", err)
	}
}

func TestStore_MessagesAndHistory(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id, _ := identity.Generate(rand.Reader)
	peer := id.Public().DestinationHash()
	for i := range 3 {
		_, err := s.AppendMessage(t.Context(), storage.HistoryEntry{
			Peer:      peer,
			Direction: "out",
			Kind:      1,
			Body:      []byte{byte('a' + i)},
			Status:    0,
			When:      time.Now().Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	hist, err := s.LoadHistory(t.Context(), peer, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("len = %d", len(hist))
	}
	// Oldest first.
	if string(hist[0].Body) != "a" {
		t.Fatalf("first body = %q", hist[0].Body)
	}
}

func TestStore_Outbox(t *testing.T) {
	t.Parallel()
	s := mkStore(t)
	id, _ := identity.Generate(rand.Reader)
	peer := id.Public().DestinationHash()
	id1, err := s.AddOutboxItem(t.Context(), peer, []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddOutboxItem(t.Context(), peer, []byte("b")); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingForPeer(t.Context(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("len = %d", len(pending))
	}
	if err := s.IncrementOutboxAttempts(t.Context(), id1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOutboxItem(t.Context(), id1); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.PendingForPeer(t.Context(), peer)
	if len(pending) != 1 {
		t.Fatalf("after delete, len = %d", len(pending))
	}
}
